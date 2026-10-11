package middleware

import (
	"encoding/json"
	"github.com/gookit/goutil/strutil"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"sort"
	"strconv"
	"strings"
)

// FillHash 填充题库的hash值
func FillHash(t *entity.Tiku) {
	if t.Answer == "" {
		t.Answer = "[]"
	} else if t.Options == "" {
		t.Options = "[]"
	}

	options := make([]string, 0)
	err := json.Unmarshal([]byte(t.Options), &options)
	if err != nil {
		t.Options = "[]" // 如果解析失败，就设置为空数组
	}
	sort.Strings(options) // 将选项排序

	sortOptionsStr, err := json.Marshal(options)
	if err != nil {
		sortOptionsStr = []byte("[]")
	}

	t.Hash = strutil.Md5(t.Question + string(sortOptionsStr) + strconv.Itoa(int(t.Type)) + strconv.Itoa(int(t.Plat)))
}

// CollectAnswer 收集没有搜索到的答案
func CollectAnswer(resp model.SearchResponse, courseName, extra, provider string) {
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
			Plat:       int32(resp.Plat),
			SourceName: sourceName,
		}
		FillHash(&t)
		err := dao.Tiku.Create(&t)
		if err == nil {
			// 注意：这里刻意不失效搜题结果缓存。
			// 本函数是每次搜题后自动触发的（高频），若在此清缓存，
			// 会把刚刚 populate 的结果立刻作废，等于关闭缓存。
			// 且本地题库走的是不被缓存的 local 通道（canUseSearchResultCache 已排除），
			// 因此这里的新增不会让缓存中的第三方答案变陈旧。
			return
		}

		// 已经收录过：补全之前缺失的字段。
		//
		// 必须只在「查询成功」时继续（历史实现写成 errNotFind != nil，
		// 条件写反导致永远拿不到已有记录、答案回填从未生效）。
		tk, findErr := dao.Tiku.Where(dao.Tiku.Hash.Eq(t.Hash)).First()
		if findErr != nil || tk == nil {
			return
		}

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
}
