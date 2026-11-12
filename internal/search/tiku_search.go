package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	aiTikuMaxCandidates = 5
	aiTikuMinScore      = 0.72
	aiTikuAmbiguityGap  = 0.08
)

type AITikuSearchTool interface {
	SearchTiku(context.Context, model.SearchRequest) (AITikuSearchResult, error)
}

type AITikuSearchCandidate struct {
	Question   string   `json:"question"`
	Type       int      `json:"type"`
	Options    []string `json:"options"`
	Answers    []string `json:"answers"`
	Source     string   `json:"source"`
	Status     string   `json:"status"`
	Evidence   string   `json:"evidence,omitempty"`
	CourseName string   `json:"courseName,omitempty"`
	Score      float64  `json:"score"`
}

type AITikuSearchResult struct {
	Query          string                  `json:"query"`
	MatchKind      string                  `json:"matchKind"`
	Ambiguous      bool                    `json:"ambiguous"`
	Candidates     []AITikuSearchCandidate `json:"candidates"`
	CandidateCount int                     `json:"candidateCount"`
}

type DBTikuSearchTool struct{ db *gorm.DB }

func NewDBTikuSearchTool(db *gorm.DB) *DBTikuSearchTool { return &DBTikuSearchTool{db: db} }

type tikuCandidateRow struct {
	Question   string
	Type       int
	Options    string
	Answer     string
	CourseName string
	Source     string
	Status     string
	Evidence   string
	Hash       string
}

func (tool *DBTikuSearchTool) SearchTiku(ctx context.Context, req model.SearchRequest) (AITikuSearchResult, error) {
	result := AITikuSearchResult{Query: strings.TrimSpace(req.Question), MatchKind: "none", Candidates: []AITikuSearchCandidate{}}
	if tool == nil || tool.db == nil {
		return result, fmt.Errorf("本地题库数据库未配置")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return result, fmt.Errorf("题目不能为空")
	}
	db := tool.db.WithContext(ctx)
	rows, err := tool.exactRows(db, req)
	if err != nil {
		return result, err
	}
	kind := "exact"
	// exact 池非空**不代表**有可用候选：题库里可能存在
	//   - Answer 为空数组（导入时参考答案缺失）
	//   - Type 与请求不兼容（题型被标错）
	//   - 选项集合冲突
	// 这类行会在下面的过滤里被逐个 continue 掉。旧实现只判 `len(rows) == 0`
	// 就决定要不要跑 fuzzy，于是「exact 池全是不可用行」时会直接返回
	// MatchKind="none" —— 相似度达标的 fuzzy 候选**根本没被查过**。
	// 这是真实的漏答（t74 实测：同一 fuzzy 候选，有污染行时 none、无污染行时 fuzzy）。
	//
	// 因此这里多一道「池里是否有可用行」的判定：不可用行不算命中，允许降级。
	if !hasUsableTikuRow(rows, req) {
		rows, err = tool.fuzzyRows(db, question)
		if err != nil {
			return result, err
		}
		kind = "fuzzy"
	}
	if len(rows) == 0 {
		return result, nil
	}
	ranked := make([]AITikuSearchCandidate, 0, len(rows))
	for _, row := range rows {
		if !compatibleTikuRow(req, question, row) {
			continue
		}
		options := decodeTikuStrings(row.Options)
		answers := decodeTikuStrings(row.Answer)
		score := 1.0
		if kind == "fuzzy" {
			score = tikuQuestionSimilarity(question, row.Question)
			if len(req.Options) > 0 && len(options) > 0 {
				score = score*0.8 + 0.2 // Compatible nonempty option sets agree after labels/ordering.
			}
			// 相似度阈值是 fuzzy 分支独有的条件，不属于「行本身是否可用」，
			// 因此刻意不放进 compatibleTikuRow（见该函数的注释）。
			if score < aiTikuMinScore {
				continue
			}
		}
		status := row.Status
		if status == "" {
			status = "unverified"
		}
		ranked = append(ranked, AITikuSearchCandidate{
			Question: row.Question, Type: row.Type, Options: options, Answers: answers,
			Source: row.Source, Status: status, Evidence: row.Evidence,
			CourseName: row.CourseName, Score: math.Round(score*1000) / 1000,
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Evidence != "" && ranked[j].Evidence == ""
	})
	if len(ranked) == 0 {
		return result, nil
	}
	if kind == "fuzzy" && len(ranked) > 1 && ranked[0].Score-ranked[1].Score < aiTikuAmbiguityGap {
		result.Ambiguous = true
	}
	answerSets := make(map[string]bool)
	for _, candidate := range ranked {
		encoded, _ := json.Marshal(candidate.Answers)
		answerSets[string(encoded)] = true
	}
	if len(answerSets) > 1 {
		result.Ambiguous = true
	}
	if len(ranked) > aiTikuMaxCandidates {
		ranked = ranked[:aiTikuMaxCandidates]
	}
	result.MatchKind = kind
	result.Candidates = ranked
	result.CandidateCount = len(ranked)
	return result, nil
}

func (tool *DBTikuSearchTool) exactRows(db *gorm.DB, req model.SearchRequest) ([]tikuCandidateRow, error) {
	hashes := util.BuildTikuHashCandidates(req.Question, req.Options, req.Type)
	var legacy []entity.Tiku
	if err := db.Where("hash IN ?", hashes).Find(&legacy).Error; err != nil {
		return nil, err
	}
	var evidence []entity.TikuAnswerEvidence
	if db.Migrator().HasTable(&entity.TikuAnswerEvidence{}) {
		if err := db.Where("question_hash IN ?", hashes).Find(&evidence).Error; err != nil {
			return nil, err
		}
	}
	rows := make([]tikuCandidateRow, 0, len(legacy)+len(evidence))
	for _, row := range legacy {
		rows = append(rows, tikuCandidateRow{Question: row.Question, Type: int(row.Type), Options: row.Options, Answer: row.Answer, CourseName: row.CourseName, Source: normalizeTikuSource(row.SourceName), Status: "unverified", Hash: row.Hash})
	}
	for _, row := range evidence {
		rows = append(rows, tikuCandidateRow{Question: row.Question, Type: row.Type, Options: row.Options, Answer: row.Answer, CourseName: row.CourseName, Source: row.Source, Status: row.Status, Evidence: row.Evidence, Hash: row.QuestionHash})
	}
	return rows, nil
}

func (tool *DBTikuSearchTool) fuzzyRows(db *gorm.DB, question string) ([]tikuCandidateRow, error) {
	terms := tikuSearchTerms(question)
	if len(terms) == 0 {
		return nil, nil
	}
	query := func(table string, evidence bool) ([]tikuCandidateRow, error) {
		condition := strings.Builder{}
		args := make([]any, 0, len(terms))
		matches := make([]string, 0, len(terms))
		for index, term := range terms {
			if index > 0 {
				condition.WriteString(" OR ")
			}
			condition.WriteString("INSTR(LOWER(question), LOWER(?)) > 0")
			args = append(args, term)
			matches = append(matches, "CASE WHEN INSTR(LOWER(question), LOWER(?)) > 0 THEN 1 ELSE 0 END")
		}
		// Rank the bounded SQL shortlist before limiting it. Recency alone can
		// hide an older near-exact question behind newer common-prefix rows.
		lengthFunction := "LENGTH"
		if db.Dialector.Name() == "mysql" {
			lengthFunction = "CHAR_LENGTH"
		}
		orderSQL := "CASE WHEN LOWER(question) = LOWER(?) THEN 1 ELSE 0 END DESC, (" + strings.Join(matches, " + ") + ") DESC, ABS(" + lengthFunction + "(question) - ?) ASC, id DESC"
		orderArgs := []any{question}
		orderArgs = append(orderArgs, args...)
		orderArgs = append(orderArgs, len([]rune(question)))
		shortlist := db.Table(table).Where(condition.String(), args...).Clauses(clause.OrderBy{Expression: clause.Expr{SQL: orderSQL, Vars: orderArgs}}).Limit(200)
		if !evidence {
			// ⚠️ 题库归档（软删除）后，已归档题**不得参与搜题匹配**（Q77-1 影响面 1）。
			// 这里是 Table(...) 裸查询：即使 GORM 会按目标结构体的 gorm.DeletedAt 自动加条件，
			// 也显式写上 —— 匹配路径的排除条件不能依赖「结构体是否带软删除字段」这种隐式约定。
			shortlist = shortlist.Where("deleted_at IS NULL")
		}
		if evidence {
			var found []entity.TikuAnswerEvidence
			err := shortlist.Find(&found).Error
			if err != nil {
				return nil, err
			}
			rows := make([]tikuCandidateRow, 0, len(found))
			for _, row := range found {
				rows = append(rows, tikuCandidateRow{Question: row.Question, Type: row.Type, Options: row.Options, Answer: row.Answer, CourseName: row.CourseName, Source: row.Source, Status: row.Status, Evidence: row.Evidence, Hash: row.QuestionHash})
			}
			return rows, nil
		}
		var found []entity.Tiku
		err := shortlist.Find(&found).Error
		if err != nil {
			return nil, err
		}
		rows := make([]tikuCandidateRow, 0, len(found))
		for _, row := range found {
			rows = append(rows, tikuCandidateRow{Question: row.Question, Type: int(row.Type), Options: row.Options, Answer: row.Answer, CourseName: row.CourseName, Source: normalizeTikuSource(row.SourceName), Status: "unverified", Hash: row.Hash})
		}
		return rows, nil
	}
	legacy, err := query(entity.TableNameTiku, false)
	if err != nil {
		return nil, err
	}
	rows := legacy
	if db.Migrator().HasTable(&entity.TikuAnswerEvidence{}) {
		evidence, err := query(entity.TableNameTikuAnswerEvidence, true)
		if err != nil {
			return nil, err
		}
		rows = append(rows, evidence...)
	}
	return rows, nil
}

// compatibleTikuRow 判断一条候选行在**题干/题型/选项/答案**四个维度上是否可用。
//
// 这是「行是否可用」的**唯一权威判定**，两个调用方：
//   - SearchTiku 的主过滤循环（决定哪些行进入 ranked）
//   - hasUsableTikuRow（决定 exact 池里有没有可用行、要不要降级跑 fuzzy）
//
// 为什么必须抽成一个函数（t126 重构，替代原先的两份复制）：
//
//	此前主循环与 hasUsableTikuRow 各写一遍同样的三个 if。
//	P0-1 给主循环加了 compatibleNegation 时，hasUsableTikuRow 就漏了，
//	造成「判定可用性」与「实际采用」两处漂移 —— 而那正是 hasUsableTikuRow
//	存在意义所要防的事，结果它自己成了漂移源。
//	抽成单一谓词后，**新增条件只需要改这一处**，结构上杜绝再次漂移。
//
// ⚠️ 刻意**不**包含相似度阈值（aiTikuMinScore）判定：
//
//	那是 fuzzy 分支独有的语义 —— exact 命中本来就不打分（score 恒 1.0），
//	若把阈值塞进来，会让「exact 池有一行低相似度」被误判为「池不可用」，
//	从而在根本不需要 fuzzy 的场景下多做一次全表 INSTR 查询。
//	主循环里该判定保留在 kind=="fuzzy" 分支内（见 SearchTiku）。
func compatibleTikuRow(req model.SearchRequest, question string, row tikuCandidateRow) bool {
	if !compatibleQuestionType(req.Type, row.Type) ||
		!compatibleMathOperators(question, row.Question) ||
		!compatibleNegation(question, row.Question) {
		return false
	}
	if !compatibleTikuOptions(req.Options, decodeTikuStrings(row.Options)) {
		return false
	}
	if len(decodeTikuStrings(row.Answer)) == 0 {
		return false
	}
	return true
}

// hasUsableTikuRow 判断一批候选行里是否存在**至少一条可用行**。
//
// 用途：决定 exact 路径要不要降级去跑 fuzzy（见 SearchTiku 的调用点）。
//
// 判定口径由 compatibleTikuRow 统一提供，本函数只负责「任一行可用」的量词。
func hasUsableTikuRow(rows []tikuCandidateRow, req model.SearchRequest) bool {
	question := strings.TrimSpace(req.Question)
	for _, row := range rows {
		if compatibleTikuRow(req, question, row) {
			return true
		}
	}
	return false
}

func decodeTikuStrings(value string) []string {
	var values []string
	if err := json.Unmarshal([]byte(value), &values); err != nil {
		return nil
	}
	return values
}

func normalizeTikuSource(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "未知来源"
}

func compatibleQuestionType(requested, stored int) bool {
	return requested == -1 || stored == -1 || requested == stored
}

// tikuOptionLabelPattern 剥掉选项开头的「标号」，让两侧选项可以逐条比对。
//
// 覆盖的标号形态（题库实际出现过的）：
//
//	A.甲   A．甲   A、甲   A:甲   A：甲   A)甲   A）甲   ← 大写 + 分隔符
//	a.甲   a)甲    a、甲                              ← 小写（题库混用）
//	(A)甲  （A）甲 [A]甲   【A】甲   （a）甲          ← 成对括号包裹
//
// ⚠️ 设计约束：**标号后必须有分隔符或闭合括号**。
// 这是不误删正当内容的关键 ——
//
//	"A类物质"    → 不动（A 后是「类」，不是分隔符）
//	"AB两项"     → 不动（两个字母不构成单个标号）
//	"A"          → 不动（只有字母，没有分隔符）
//
// 若放宽成 `^[A-Za-z]` 一律删，上述三种正常选项都会被削掉首字母，
// 后果是 compatibleTikuOptions 判不等 → 整条候选被丢弃（比漏标号更糟）。
//
// 改动依据（t67 实测 + t82 复核）：旧正则 `^[A-Z][.．:：、)]\s*` 只认大写，
// 且不认成对括号，导致 `a.甲` / `（A）甲` / `【A】甲` 全部剥不掉 →
// 与 `A.甲` 形式的同一选项判为不同 → 整条候选被丢。
var tikuOptionLabelPattern = regexp.MustCompile(
	// 形态一：字母 + 分隔符（大写/小写都认）
	`^(?:[A-Za-z][.．:：、)）]\s*` +
		// 形态二：成对括号包裹的单字母（半角/全角方括号与圆括号）
		`|[（(][A-Za-z][）)]\s*` +
		`|[【\[](?:[A-Za-z]|[A-Za-z]、)[】\]]\s*)`,
)
var tikuNumberPattern = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

func normalizedTikuOptions(options []string) []string {
	values := make([]string, len(options))
	for index, option := range options {
		values[index] = normalizeTikuText(tikuOptionLabelPattern.ReplaceAllString(strings.TrimSpace(option), ""))
	}
	sort.Strings(values)
	return values
}

func compatibleTikuOptions(requested, stored []string) bool {
	if len(requested) == 0 || len(stored) == 0 {
		return true
	}
	if len(requested) != len(stored) {
		return false
	}
	left, right := normalizedTikuOptions(requested), normalizedTikuOptions(stored)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// normalizeTikuText 把题干/选项归一成「可比较」的形式，供相似度与召回使用。
//
// 做三件事：全角转半角（借 util.FullWidthStrToHalfWidthStr）、
// 小写化、把连续空白折叠成单个空格。
//
// ⚠️ 为什么**不**直接调 util.FormatString：
//
//	FormatString 末尾有 `TrimRightFunc(",.?:!;")`，会把「1+1=?」裁成「1+1=」，
//	把「3!=」裁成「3=」—— 数学表达式的结尾被吃掉，相似度与 operator 比对都会失真。
//	实测（t82）：FormatString("1+1=?") == "1+1="，而正确答案应保留问号。
//	所以这里只借它的**全角转半角**这一段，不借它裁尾标点的副作用。
//
// ⚠️ 为什么**不**裁末尾标点：
//
//	「下列哪个是质数」与「下列哪个是质数？」在全角转半角后 sim=0.9231，
//	已远高于 aiTikuMinScore=0.72，不需要靠裁标点来命中。
//	裁标点只会引入「数学表达式被破坏」的风险而不带来额外召回。
//
// ⚠️ 本函数**不参与**题库 hash 计算（hash 走 pkg/util/normalize.go 的
// NormalizeCacheKeyText → FormatString）。本函数的 4 个调用方全部在
// internal/search 包内：normalizedTikuOptions、tikuQuestionSimilarity（×2）、
// tikuSearchTerms。因此修改本函数**不会**让存量题库的 hash 失效。
func normalizeTikuText(value string) string {
	// 全角转半角：`ＡＢＣ` → `ABC`、`３＋５` → `3+5`、全角空格 → 半角空格。
	// 不借用 FormatString 的其余步骤（见上方说明）。
	value = util.FullWidthStrToHalfWidthStr(value)

	var out strings.Builder
	space := true
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsSpace(r) {
			if !space {
				out.WriteByte(' ')
			}
			space = true
			continue
		}
		out.WriteRune(r)
		space = false
	}
	return strings.TrimSpace(out.String())
}

func tikuQuestionSimilarity(left, right string) float64 {
	a, b := tikuBigrams(normalizeTikuText(left)), tikuBigrams(normalizeTikuText(right))
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for gram := range a {
		if _, ok := b[gram]; ok {
			shared++
		}
	}
	return float64(2*shared) / float64(len(a)+len(b))
}

func tikuBigrams(value string) map[string]struct{} {
	runes := []rune(value)
	grams := make(map[string]struct{})
	if len(runes) == 1 {
		grams[string(runes[0])] = struct{}{}
		return grams
	}
	for index := 0; index+1 < len(runes); index++ {
		grams[string(runes[index:index+2])] = struct{}{}
	}
	return grams
}

func tikuSearchTerms(question string) []string {
	question = normalizeTikuText(question)
	if question == "" {
		return nil
	}
	runes := []rune(question)
	grams := make([]string, 0, len(runes))
	seen := map[string]bool{}
	for index := 0; index+1 < len(runes); index++ {
		if unicode.IsSpace(runes[index]) || unicode.IsSpace(runes[index+1]) {
			continue
		}
		term := string(runes[index : index+2])
		if !seen[term] {
			seen[term] = true
			grams = append(grams, term)
		}
	}
	// Include the whole question, then sample unique bigrams across the entire
	// text rather than spending the shortlist on a generic opening phrase.
	terms := []string{question}
	const maxBigrams = 11
	if len(grams) <= maxBigrams {
		for _, gram := range grams {
			if gram != question {
				terms = append(terms, gram)
			}
		}
		return terms
	}
	for index := 0; index < maxBigrams; index++ {
		terms = append(terms, grams[index*(len(grams)-1)/(maxBigrams-1)])
	}
	return terms
}

// compatibleMathOperators 要求两侧的「运算符序列」与「数字序列」都一致。
//
// 用途：防止「x < 4」被匹配到「x > 5」这类语义相反的题目。
//
// ⚠️ 两侧必须先做全角转半角，否则全角写法的运算符与数字会被漏掉：
//
//	tikuMathOperators 逐 rune 匹配 ASCII 运算符集，全角 ＋ 不在集合里 → 抽不出来；
//	tikuNumberPattern 是 `[0-9]+`，全角 ３５ 也抽不出来。
//	实测（t82）：compatibleMathOperators("计算3+5", "计算３＋５") 在未归一前 = false，
//	会让全角写法的题干被整条丢弃（即使相似度已达 1.0）。
//
// 只借 FullWidthStrToHalfWidthStr，不借 FormatString —— 后者裁尾标点，
// 会把「1+1=?」变成「1+1=」，改变数字/运算符序列的语义。
func compatibleMathOperators(left, right string) bool {
	left = util.FullWidthStrToHalfWidthStr(left)
	right = util.FullWidthStrToHalfWidthStr(right)
	leftOps, rightOps := tikuMathOperators(left), tikuMathOperators(right)
	return leftOps == rightOps && strings.Join(tikuNumberPattern.FindAllString(left, -1), "|") == strings.Join(tikuNumberPattern.FindAllString(right, -1), "|")
}

// tikuNegationSuffixes 是题干里「否定问法」的收尾模式。
//
// 观察到的真实问法都集中在句尾：`不正确的是`、`不属于…的是`、`错误的是`、
// `不能…的是`、`无法…的是`。把判定锚在**句尾窗口**而不是全文任意位置，
// 是为了避开「不等式」「不确定性」「不同」这类把「不」当构词成分的正常题面
// ——它们不会出现在句尾的 `…的是` 结构里。
var tikuNegationSuffixes = []string{
	"不正确的是", "不正确的有", "不正确的",
	"错误的是", "错误的有", "错误的",
	"不属于",
	"不包括",
	"不符合",
	"不正确",
	"不对",
	"不能",
	"不会",
	"不可",
	"无法",
	"没有",
	"并非",
	"不是",
	"不应",
	"不该",
	"不需要",
	"不可能",
	"不一定",
}

// tikuNegationSuffixWindow 是句尾判定窗口的长度（rune 数）。
//
// 取 12 是因为最长需要覆盖的模式是「下列不属于生物的是」这类收尾
// （`不属于生物的是` 共 7 字），留一倍余量。
const tikuNegationSuffixWindow = 12

// tikuQuestionNegated 判断题干是否为否定问法。
//
// 只在**句尾窗口**里找否定模式，找到任一即认定是否定问法。
func tikuQuestionNegated(question string) bool {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" {
		return false
	}
	runes := []rune(trimmed)
	start := len(runes) - tikuNegationSuffixWindow
	if start < 0 {
		start = 0
	}
	tail := string(runes[start:])
	for _, suffix := range tikuNegationSuffixes {
		if strings.Contains(tail, suffix) {
			return true
		}
	}
	return false
}

// compatibleNegation 要求两侧题干在「是否否定问法」这一点上一致。
//
// 为什么必须有这一层（线上实测，见 t67 审查）：
//
//	sim("...叙述正确的是", "...不正确的是") = 0.8966
//	sim("以下说法正确",   "以下说法不正确")  = 0.7273
//	sim("可以发生反应",   "不可以发生反应")  = 0.9091
//
// 全部高于 aiTikuMinScore=0.72 —— bigram 把「不」稀释进二十来个 bigram 里
// 几乎无影响。而这是**唯一会主动返回错误答案**的匹配缺陷（其余都是漏答，
// 用户能感知「没答案」，感知不到「答错了」）。
//
// 与 compatibleMathOperators 同一范式：抽关键特征集合，不等则拒。
// **刻意不做降分**：降分需要重新标定 0.72，会连带影响所有正常题目的召回；
// 否定是强区分信号，只有「一致」与「不一致」两种状态。
//
// 注意「两侧都否定」必须判为兼容：
//
//	"下列说法不正确的是" vs "下列说法不正确的是" → 同一题，必须匹配
//
// 因此比较的是**布尔值是否相等**，不是「任一侧含否定就拒」。
func compatibleNegation(left, right string) bool {
	return tikuQuestionNegated(left) == tikuQuestionNegated(right)
}

func tikuMathOperators(value string) string {
	const operators = "=<>≤≥≠+-−×÷*/^%√∫∑!∈∉∋∌∪∩⊂⊃⊆⊇⊄⊅⊈⊉∧∨¬⊕⊗⇒⇔→↔∀∃∄"
	var out strings.Builder
	for _, r := range value {
		if strings.ContainsRune(operators, r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func marshalAITikuSearchResult(result AITikuSearchResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"error":"本地题库结果编码失败"}`
	}
	return string(encoded)
}

func parseAITikuSearchToolQuery(arguments string) string {
	var input struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal([]byte(arguments), &input)
	return strings.TrimSpace(input.Question)
}

func countAITikuSearchResults(content string) int {
	var result AITikuSearchResult
	if json.Unmarshal([]byte(content), &result) != nil {
		return 0
	}
	return result.CandidateCount
}

func (in *ChatGptClient) executeAITikuSearchTool(ctx context.Context, call openAIToolCall, webSearches, localSearches *int, maxSearches int, cache map[string]string, contentBytes *int, usage *AISearchUsage, roundContext ...int) (string, bool) {
	failure := func(message string) string {
		encoded, _ := json.Marshal(map[string]string{"error": message})
		return string(encoded)
	}
	started := time.Now()
	callUsage := AITikuSearchCallUsage{Status: "error"}
	executed := false
	finish := func() {
		if !executed {
			in.appendAISearchTrace(usage, AISearchTrace{Stage: "本地题库检索未执行", Query: callUsage.Query, Error: callUsage.Error})
		}
		callUsage.DurationMs = time.Since(started).Milliseconds()
		if usage != nil {
			usage.TikuSearchCalls = append(usage.TikuSearchCalls, callUsage)
		}
	}
	if call.Type != "function" || call.Function.Name != "tiku_search" || in.TikuSearchTool == nil {
		callUsage.Error = "本地题库检索工具不可用"
		finish()
		return failure(callUsage.Error), false
	}
	if len(call.Function.Arguments) > aiToolArgumentLimit("tiku_search") {
		callUsage.Error = "本地题库检索参数超过大小限制"
		finish()
		return failure(callUsage.Error), false
	}
	var input struct {
		Question   string   `json:"question"`
		Type       *int     `json:"type"`
		Options    []string `json:"options"`
		CourseName string   `json:"courseName"`
	}
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		callUsage.Error = "本地题库检索参数不是有效JSON"
		finish()
		return failure(callUsage.Error), false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		callUsage.Error = "本地题库检索参数包含多份JSON"
		finish()
		return failure(callUsage.Error), false
	}
	input.Question = strings.TrimSpace(input.Question)
	callUsage.Query = input.Question
	if input.Question == "" || len([]rune(input.Question)) > 12000 || len(input.Options) > 100 {
		callUsage.Error = "题干必须为1至12000个字符，选项不得超过100项"
		finish()
		return failure(callUsage.Error), false
	}
	questionType := -1
	if input.Type != nil {
		questionType = *input.Type
	}
	encoded, _ := json.Marshal(input)
	cacheKey := string(encoded)
	budgetFailure := failure("本地题库结果超过累计资料预算，请根据已有资料直接作答。")
	if previous, exists := cache[cacheKey]; exists {
		if previous == budgetFailure || !consumeAIToolContentBudget(previous, contentBytes) {
			return budgetFailure, true
		}
		return previous, false
	}
	if contentBytes != nil && *contentBytes >= aiToolMaxContentBytes {
		return budgetFailure, true
	}
	if *webSearches+*localSearches >= maxSearches {
		callUsage.Error = "搜索调用预算已用尽"
		finish()
		return failure(callUsage.Error), true
	}
	*localSearches++
	executed = true
	trace := AISearchTrace{Stage: "本地题库检索开始", Query: input.Question, SearchCount: *webSearches + *localSearches, SearchBudget: maxSearches, RemainingSearches: maxSearches - *webSearches - *localSearches}
	if len(roundContext) > 0 {
		trace.Round, trace.ModelCallCount = roundContext[0], roundContext[0]
	}
	if len(roundContext) > 1 {
		trace.ModelCallBudget = roundContext[1]
		trace.RemainingModelCalls = roundContext[1] - trace.ModelCallCount
	}
	in.appendAISearchTrace(usage, trace)
	result, err := in.TikuSearchTool.SearchTiku(ctx, model.SearchRequest{Question: input.Question, Type: questionType, Options: input.Options, CourseName: input.CourseName})
	if err != nil {
		callUsage.Error = logger.RedactSecrets(err.Error(), in.APIKey)
		finish()
		return failure("本地题库检索失败，请根据已有依据作答。"), true
	}
	callUsage.Status = "success"
	callUsage.MatchKind = result.MatchKind
	callUsage.CandidateCount = result.CandidateCount
	// 同步模糊命中标记，让运维能看到「这条查询是模糊命中且有歧义」。
	//
	// 位置很关键：这一行必须在 `SearchTiku` 返回成功之后、`finish()` 之前。
	//   · SearchTiku 失败时上面的 err 分支已经 return，走不到这里 ——
	//     所以错误路径上的 callUsage.Ambiguous 保持零值 false，
	//     不会被误读成「查过且不模糊」。
	//   · finish() 会把这个结构体按值追加进 usage.TikuSearchCalls，
	//     所以必须在 finish() 之前赋值，之后再改就只改到局部副本。
	// Ambiguous 由 SearchTiku 在成功路径上计算（tiku_search.go 的 fuzzy 分支），
	// 这里只做透传，不重新判定 —— 判据只有一处，避免两侧口径漂移。
	callUsage.Ambiguous = result.Ambiguous
	if len(result.Candidates) > aiTikuMaxCandidates {
		result.Candidates = result.Candidates[:aiTikuMaxCandidates]
	}
	// Normalize a copy; the tool can return shared/cached DB results.
	result.Candidates = append([]AITikuSearchCandidate{}, result.Candidates...)
	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		candidate.Question = truncateAITikuText(candidate.Question, 12000)
		candidate.Source = truncateAITikuText(candidate.Source, 256)
		candidate.Evidence = truncateAITikuText(candidate.Evidence, 4096)
		candidate.CourseName = truncateAITikuText(candidate.CourseName, 256)
		if len(candidate.Options) > 100 {
			candidate.Options = candidate.Options[:100]
		}
		if len(candidate.Answers) > 100 {
			candidate.Answers = candidate.Answers[:100]
		}
		candidate.Options = append([]string(nil), candidate.Options...)
		candidate.Answers = append([]string(nil), candidate.Answers...)
		for optionIndex := range candidate.Options {
			candidate.Options[optionIndex] = truncateAITikuText(candidate.Options[optionIndex], 2048)
		}
		for answerIndex := range candidate.Answers {
			candidate.Answers[answerIndex] = truncateAITikuText(candidate.Answers[answerIndex], 2048)
		}
	}
	// This completed query remains billable even if its evidence must be
	// trimmed or omitted. Never represent omitted candidates as a real miss.
	finish()
	for {
		result.CandidateCount = len(result.Candidates)
		content := marshalAITikuSearchResult(result)
		if consumeAIToolContentBudget(content, contentBytes) {
			cache[cacheKey] = content
			return content, false
		}
		if len(result.Candidates) <= 1 {
			break
		}
		result.Candidates = result.Candidates[:len(result.Candidates)-1]
	}
	// Do not repeat a real lookup if its full shortlist did not fit.
	// Budget failures stop tool execution before the next model round.
	cache[cacheKey] = budgetFailure
	return budgetFailure, true
}

func truncateAITikuText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}
