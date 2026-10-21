package middleware

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm/clause"
)

type CollectAnswerResult struct {
	ID     int32
	Stored bool
}

// FillHash 填充题库的hash值。
//
// 公式集中在 pkg/util.BuildTikuHash：题面、排序后的选项、题型三者决定题目身份。
// 注意写入必须与查询用**同一个**公式，否则新入库的题目查不出来
// （历史实现写入用未格式化题面、查询用格式化题面，两者对不上）。
func FillHash(t *entity.Tiku) {
	if t.Answer == "" {
		t.Answer = "[]"
	} else if t.Options == "" {
		t.Options = "[]"
	}

	options := make([]string, 0)
	if err := json.Unmarshal([]byte(t.Options), &options); err != nil {
		t.Options = "[]" // 如果解析失败，就设置为空数组
		options = options[:0]
	}

	t.Hash = util.BuildTikuHash(t.Question, options, int(t.Type))
}

// CollectAnswer 收集没有搜索到的答案
func CollectAnswer(resp model.SearchResponse, courseName, extra, provider string) CollectAnswerResult {
	resultInfo := CollectAnswerResult{}
	sort.Strings(resp.Options) // 将选项排序
	opts, err := json.Marshal(resp.Options)
	if err != nil {
		opts = []byte("[]")
	}
	ans := "[]"
	if len(resp.Answer.AnswerKey) > 0 && len(resp.Answer.BestAnswer) > 0 { // 客观题能直接找到answerKey
		marshal, _ := json.Marshal(resp.Answer.BestAnswer)
		ans = string(marshal)
	} else if len(resp.Answer.BestAnswer) > 0 && resp.Type != 3 && resp.Type != 0 && resp.Type != 1 { // 排除客观题之后依然有答案
		marshal, _ := json.Marshal(resp.Answer.BestAnswer)
		ans = string(marshal)
	}
	sourceName := strings.TrimSpace(service.ResolveSearchAccountProviderLabel(provider))
	recordEmptyAnswer := false
	if mgr := manager.GetManager(); mgr != nil {
		recordEmptyAnswer = mgr.GetConfig().RecordEmptyAnswer
	}
	// 记录空答案或者有答案才会被记录
	if recordEmptyAnswer || ans != "[]" {
		t := entity.Tiku{
			CourseName: courseName,
			Extra:      extra,
			Type:       int32(resp.Type),
			Question:   resp.Question,
			Answer:     ans,
			Options:    string(opts),
			SourceName: sourceName,
		}
		FillHash(&t)
		// 用 upsert 语义写入：撞唯一键（同一道题已收录）时**不报错、不覆盖**。
		//
		// 历史实现是「先 Create，失败再查库补空字段」，于是每搜到一道重复题，
		// 数据库日志都会记一条 `UNIQUE constraint failed: tiku.hash, tiku.course_name`
		// 的 ERROR（线上 8 次收录里刷了 3 条），既误导排障、也浪费一次失败写入。
		// 见 AGENTS.md 第 26 条。
		result := dao.Tiku.UnderlyingDB().Clauses(clause.OnConflict{DoNothing: true}).Create(&t)
		if result.Error != nil {
			return resultInfo
		}
		if result.RowsAffected > 0 {
			// 注意：这里刻意不失效搜题结果缓存。
			// 本函数是每次搜题后自动触发的（高频），若在此清缓存，
			// 会把刚刚 populate 的结果立刻作废，等于关闭缓存。
			// 且本地题库走的是不被缓存的 local 通道（canUseSearchResultCache 已排除），
			// 因此这里的新增不会让缓存中的第三方答案变陈旧。
			resultInfo.ID = t.ID
			resultInfo.Stored = true
			return resultInfo
		}

		// 已经收录过（撞唯一键、本次没写入）：补全之前缺失的字段。
		//
		// 必须只在「查询成功」时继续（历史实现写成 errNotFind != nil，
		// 条件写反导致永远拿不到已有记录、答案回填从未生效）。
		// 冲突目标就是 (hash, course_name)，因此按这两列定位已有行。
		tk, findErr := dao.Tiku.Where(dao.Tiku.Hash.Eq(t.Hash), dao.Tiku.CourseName.Eq(t.CourseName)).First()
		if findErr != nil || tk == nil {
			return resultInfo
		}
		resultInfo.ID = tk.ID
		resultInfo.Stored = true

		if tk.Answer == "[]" {
			_, _ = dao.Tiku.Where(dao.Tiku.ID.Eq(tk.ID)).Update(dao.Tiku.Answer, ans)
		}
		if tk.Extra == "" && extra != "" {
			_, _ = dao.Tiku.Where(dao.Tiku.ID.Eq(tk.ID)).Update(dao.Tiku.Extra, extra)
		}
		if tk.CourseName == "" && courseName != "" {
			_, _ = dao.Tiku.Where(dao.Tiku.ID.Eq(tk.ID)).Update(dao.Tiku.CourseName, courseName)
		}
		if strings.TrimSpace(tk.SourceName) == "" && sourceName != "" {
			_, _ = dao.Tiku.Where(dao.Tiku.ID.Eq(tk.ID)).Update(dao.Tiku.SourceName, sourceName)
		}
	}
	return resultInfo
}
