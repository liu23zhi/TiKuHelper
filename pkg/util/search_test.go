package util

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newInstrBindingTestDB 建一个带特殊字符数据的临时库，用于验证匹配在真实驱动下生效。
func newInstrBindingTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:instr-binding-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec("CREATE TABLE probe (v text)").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}

	samples := []string{
		"rcg_plain",
		"rcg_under_score",
		"pct%value",
		"has!bang",
		"has~tilde",
		"普通题目",
		"UPPER-Case",
	}
	for _, sample := range samples {
		if err := db.Exec("INSERT INTO probe (v) VALUES (?)", sample).Error; err != nil {
			t.Fatalf("insert %q: %v", sample, err)
		}
	}
	return db
}

func queryInstrProbe(t *testing.T, db *gorm.DB, keyword string) []string {
	t.Helper()

	condition, args := BuildInstrCondition(keyword, "COALESCE(v, '')")
	if condition == "" {
		return nil
	}

	var rows []struct {
		V string `gorm:"column:v"`
	}
	if err := db.Raw("SELECT v FROM probe WHERE "+condition, args...).Scan(&rows).Error; err != nil {
		t.Fatalf("query %q: %v", keyword, err)
	}

	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.V)
	}
	return out
}

// 关键回归测试：不要用 LIKE 转义来实现「按字面量匹配」。
//
// 历史坑：`LIKE '%kw%'` 需要 ESCAPE 才能把用户输入的 % / _ 当字面量，
// 但 glebarez/sqlite 下 ESCAPE 会静默失效（SQLite 里双引号是标识符），
// 导致输入 "_" 匹配到全部记录。改用 INSTR 后不存在通配符概念，本测试锁定该行为。
func TestBuildInstrConditionMatchesWildcardCharactersLiterally(t *testing.T) {
	db := newInstrBindingTestDB(t)

	cases := []struct {
		name    string
		keyword string
		want    []string
	}{
		// "%" 必须按字面量匹配，只命中真的含百分号的记录。
		{name: "percent is literal", keyword: "%", want: []string{"pct%value"}},
		// "_" 必须按字面量匹配，只命中真的含下划线的记录。
		{name: "underscore is literal", keyword: "_", want: []string{"rcg_plain", "rcg_under_score"}},
		{name: "underscore with prefix", keyword: "rcg_under", want: []string{"rcg_under_score"}},
		// "!" 与 "~" 曾经因为「转义符本身出现在关键词里」而出错。
		{name: "bang is literal", keyword: "has!bang", want: []string{"has!bang"}},
		{name: "tilde is literal", keyword: "has~tilde", want: []string{"has~tilde"}},
		// 普通子串搜索仍然工作。
		{name: "plain substring", keyword: "rcg", want: []string{"rcg_plain", "rcg_under_score"}},
		{name: "chinese substring", keyword: "题目", want: []string{"普通题目"}},
		// 大小写不敏感（两侧都套 LOWER）。
		{name: "case insensitive", keyword: "upper-case", want: []string{"UPPER-Case"}},
		{name: "no match", keyword: "不存在的内容", want: nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := queryInstrProbe(t, db, testCase.keyword)
			if len(got) != len(testCase.want) {
				t.Fatalf("keyword %q: got %v, want %v", testCase.keyword, got, testCase.want)
			}
			seen := make(map[string]struct{}, len(got))
			for _, value := range got {
				seen[value] = struct{}{}
			}
			for _, value := range testCase.want {
				if _, ok := seen[value]; !ok {
					t.Fatalf("keyword %q: expected %q in %v", testCase.keyword, value, got)
				}
			}
		})
	}
}

// 关键词里同时包含 % 与 _ 时，两者都按字面量处理（没有记录同时含这两个字符，故不命中）。
func TestBuildInstrConditionHandlesMixedWildcards(t *testing.T) {
	db := newInstrBindingTestDB(t)

	if got := queryInstrProbe(t, db, "%_"); len(got) != 0 {
		t.Fatalf("expected no matches for %%_, got %v", got)
	}
}

// 空关键词必须返回空条件，让调用方跳过整个筛选（否则会退化成「匹配所有非空值」）。
func TestBuildInstrConditionSkipsEmptyKeyword(t *testing.T) {
	for _, keyword := range []string{"", "   ", "\t\n"} {
		condition, args := BuildInstrCondition(keyword, "COALESCE(v, '')")
		if condition != "" || args != nil {
			t.Fatalf("expected empty condition for %q, got %q / %#v", keyword, condition, args)
		}
	}

	// 没有列时不生成条件。
	if condition, _ := BuildInstrCondition("x"); condition != "" {
		t.Fatalf("expected empty condition without columns, got %q", condition)
	}
}

// 每个列分支都要成对带上参数，列数变了参数数量也要跟着变。
func TestBuildInstrConditionProducesOneArgPerColumn(t *testing.T) {
	condition, args := BuildInstrCondition("牛顿", "COALESCE(a, '')", "COALESCE(b, '')", "COALESCE(c, '')")

	if got := strings.Count(condition, "INSTR("); got != 3 {
		t.Fatalf("expected 3 INSTR branches, got %d in %q", got, condition)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d (%#v)", len(args), args)
	}
	for _, arg := range args {
		if arg != "牛顿" {
			t.Fatalf("unexpected arg %#v", arg)
		}
	}
}

func TestNormalizeSearchKeywordTruncatesByRunes(t *testing.T) {
	long := strings.Repeat("题", SearchKeywordMaxLength+20)
	if got := len([]rune(NormalizeSearchKeyword(long))); got != SearchKeywordMaxLength {
		t.Fatalf("expected %d runes, got %d", SearchKeywordMaxLength, got)
	}
	if got := NormalizeSearchKeyword("  牛顿  "); got != "牛顿" {
		t.Fatalf("expected trimmed keyword, got %q", got)
	}
	if got := NormalizeSearchKeyword("   "); got != "" {
		t.Fatalf("expected empty keyword, got %q", got)
	}
}
