package search

import (
	"context"
	"encoding/json"
	"log"

	"github.com/go-resty/resty/v2"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// DB mysql 或者sqlite3
type dBSearch struct{}

var defaultDBSearch = &dBSearch{}

// GetDBSearch 获取DB搜索实例
func GetDBSearch() Search {
	return defaultDBSearch
}
func (in *dBSearch) getHTTPClient() *resty.Client {
	panic("implement me")
}

// SearchAnswer 搜索答案
func (in *dBSearch) SearchAnswer(req model.SearchRequest) (answer [][]string, err error) {
	answer = make([][]string, 0)

	// hash 公式集中在 pkg/util.BuildTikuHashCandidates：
	// 返回「当前公式 + 旧 plat 公式 + 更早的未格式化题面变体」，
	// 保证公式切换与灰度共存期间已有题库不会失联。
	hashCandidates := util.BuildTikuHashCandidates(req.Question, req.Options, req.Type)

	requestContext := req.RequestContext
	if requestContext == nil {
		requestContext = context.Background()
	}
	ctx := dbqueue.WithPriority(requestContext, dbqueue.PriorityHigh)
	tiku := dao.Tiku
	tx := tiku.WithContext(ctx).Where(tiku.Hash.In(hashCandidates...))

	// 如果提供了courseName，则也匹配courseName
	if req.CourseName != "" {
		tx = tx.Where(tiku.CourseName.Eq(req.CourseName))
	}

	find, err := tx.Find()
	if err != nil {
		return nil, err
	}
	// 如果数据库中没有extra那么自动补全他
	if len(find) == 1 && find[0].Extra == "" {
		_, err1 := tiku.WithContext(ctx).Where(tiku.ID.Eq(find[0].ID)).Update(tiku.Extra, req.Extra)
		if err1 != nil {
			log.Println("更新extra失败", err1)
		}
	}
	for i := range find {
		var answers []string // 最后所有的答案的二维数组
		err := json.Unmarshal([]byte(find[i].Answer), &answers)
		if err != nil {
			continue
		}
		if len(answers) > 0 {
			answer = append(answer, answers)
		}
	}
	return answer, nil
}
