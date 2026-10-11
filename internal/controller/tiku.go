package controller

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Page 分页
type Page struct {
	PageNo   int            `json:"pageNo" form:"pageNo"`
	PageSize int            `json:"pageSize" form:"pageSize"`
	Total    int64          `json:"total" form:"total"`
	Items    []*entity.Tiku `json:"items" form:"items"`
}

// SearchValue 搜索参数
type SearchValue struct {
	PageNo              int    `json:"pageNo" form:"pageNo"`
	PageSize            int    `json:"pageSize" form:"pageSize"`
	Source              int32  `json:"source" form:"source"`
	Extra               string `json:"extra" form:"extra"`
	OnlyShowEmptyAnswer bool   `json:"onlyShowEmptyAnswer" form:"onlyShowEmptyAnswer"`
	Question            string `json:"question" form:"question"`
	CourseName          string `json:"courseName" form:"courseName"`

	Type int32 `json:"type" form:"type"`
}

func normalizeQuestionSourceName(sourceName string) string {
	trimmed := strings.TrimSpace(sourceName)
	if trimmed == "" {
		return "人工导入"
	}
	return trimmed
}

type importQuestionsRequest struct {
	DryRun    bool           `json:"dryRun"`
	Overwrite bool           `json:"overwrite"`
	Items     []*entity.Tiku `json:"items"`
}

type importQuestionsResponse struct {
	ExistingIDs []int32 `json:"existingIds"`
	Created     int     `json:"created"`
	Updated     int     `json:"updated"`
	Skipped     int     `json:"skipped"`
}

func importQuestionsWithOverwrite(items []*entity.Tiku, overwrite bool, dryRun bool) (importQuestionsResponse, error) {
	result := importQuestionsResponse{
		ExistingIDs: make([]int32, 0),
	}

	for _, item := range items {
		if item == nil {
			continue
		}

		item.SourceName = normalizeQuestionSourceName(item.SourceName)
		middleware.FillHash(item)

		existing := false
		if item.ID > 0 {
			count, err := dao.Tiku.Where(dao.Tiku.ID.Eq(item.ID)).Count()
			if err != nil {
				return result, err
			}
			if count > 0 {
				existing = true
				result.ExistingIDs = append(result.ExistingIDs, item.ID)
			}
		}

		if dryRun {
			continue
		}

		if existing {
			if !overwrite {
				result.Skipped++
				continue
			}

			if err := dao.Tiku.UnderlyingDB().Model(&entity.Tiku{}).Where("id = ?", item.ID).Updates(map[string]interface{}{
				"question":    item.Question,
				"type":        item.Type,
				"options":     item.Options,
				"answer":      item.Answer,
				"hash":        item.Hash,
				"course_name": item.CourseName,
				"extra":       item.Extra,
				"source_name": item.SourceName,
			}).Error; err != nil {
				return result, err
			}
			result.Updated++
			continue
		}

		if err := dao.Tiku.Create(item); err != nil {
			return result, err
		}
		result.Created++
	}

	return result, nil
}

// ImportQuestions 通过 JSON 导入题库
func ImportQuestions(c *gin.Context) {
	var req importQuestionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	result, err := importQuestionsWithOverwrite(req.Items, req.Overwrite, req.DryRun)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "服务器错误",
		})
		return
	}

	// 题库内容变了，缓存的搜题结果可能已过时（dryRun 不写库，无需失效）。
	if !req.DryRun && (result.Created > 0 || result.Updated > 0) {
		invalidateSearchResultCaches("import_questions")
	}

	c.JSON(http.StatusOK, result)
}

// GetQuestions 获取题库
func GetQuestions(c *gin.Context) {
	var searchValue SearchValue
	err := c.ShouldBindJSON(&searchValue)
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}
	user, _ := c.Get("user")
	if searchValue.PageSize > 10 {
		currentUser := user.(*entity.User)
		r, _ := json.Marshal(searchValue)
		l := entity.Log{
			Qid:        0,
			Action:     3,
			UserID:     currentUser.ID,
			Username:   currentUser.Username,
			CreateTime: time.Now(),
			OldAnswer:  string(r),
		}
		dao.Log.Create(&l)
	}

	if searchValue.PageSize > 100 {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "服务器错误",
		})
		return
	}

	tx := dao.Tiku.Order(dao.Tiku.ID.Desc())
	if searchValue.Question != "" {
		tx.Where(dao.Tiku.Question.Like("%" + searchValue.Question + "%"))
	}
	if searchValue.Extra != "" {
		tx.Where(dao.Tiku.Extra.Like(searchValue.Extra))
	}

	if searchValue.CourseName != "" {
		tx.Where(dao.Tiku.CourseName.Eq(searchValue.CourseName))
	}

	if searchValue.Source != 0 {
		if searchValue.Source == 1 {
			tx.Where(dao.Tiku.SourceName.Eq("人工导入"))
		} else if searchValue.Source == 2 {
			tx.Where(dao.Tiku.SourceName.Neq("人工导入"))
		}
	}

	if searchValue.Type != -1 {
		tx.Where(dao.Tiku.Type.Eq(searchValue.Type))
	}

	if searchValue.OnlyShowEmptyAnswer {
		tx.Where(dao.Tiku.Answer.Eq("[]"))
	}

	items, total, err := tx.FindByPage(searchValue.PageNo*searchValue.PageSize, searchValue.PageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "服务器错误",
		})
		return
	}

	c.JSON(http.StatusOK, Page{
		PageNo:   searchValue.PageNo,
		PageSize: searchValue.PageSize,
		Total:    total,
		Items:    items,
	})
}

// UpdateQuestions 更新题库
func UpdateQuestions(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	var tiku *entity.Tiku
	err = c.ShouldBindJSON(&tiku)
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}
	// 找到旧答案
	oldTiku, err := dao.Tiku.Where(dao.Tiku.ID.Eq(int32(id))).First()

	middleware.FillHash(tiku) // 更新题库的时候重新计算hash值
	updates, err := dao.Tiku.Where(dao.Tiku.ID.Eq(int32(id))).Updates(tiku)
	if err != nil {
		c.JSON(500, gin.H{
			"message": "服务器错误",
		})
		return
	}
	if updates.RowsAffected == 1 {
		user, _ := c.Get("user")
		currentUser := user.(*entity.User)
		l := entity.Log{
			Qid:        oldTiku.ID,
			Action:     1,
			UserID:     currentUser.ID,
			Username:   currentUser.Username,
			CreateTime: time.Now(),
			OldAnswer:  oldTiku.Answer,
			NewAnswer:  tiku.Answer,
		}
		dao.Log.Create(&l)
		// 人工修正了题目答案，缓存的旧答案必须立即作废。
		invalidateSearchResultCaches("update_question")
	}
	c.JSON(200, gin.H{
		"data": updates,
	})
}

// DeleteQuestion 删除题目
func DeleteQuestion(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}

	// 记录删除日志
	oldTiku, err := dao.Tiku.Where(dao.Tiku.ID.Eq(int32(id))).First()
	marshal, _ := json.Marshal(oldTiku)
	user, _ := c.Get("user")
	currentUser := user.(*entity.User)
	l := entity.Log{
		Qid:        oldTiku.ID,
		Action:     2,
		UserID:     currentUser.ID,
		Username:   currentUser.Username,
		CreateTime: time.Now(),
		OldAnswer:  string(marshal),
	}
	dao.Log.Create(&l)

	dao.Tiku.Where(dao.Tiku.ID.Eq(int32(id))).Delete()
	// 题目被删除，之前基于它建立的搜题结果缓存必须作废。
	invalidateSearchResultCaches("delete_question")
	c.JSON(200, gin.H{
		"message": "删除成功",
	})
}

// CreateQuestion 创建题目
func CreateQuestion(c *gin.Context) {
	var tikus []*entity.Tiku
	err := c.ShouldBindJSON(&tikus)
	if err != nil {
		c.JSON(400, gin.H{
			"message": "参数错误",
		})
		return
	}
	var count = 0
	for _, tiku := range tikus {
		t := tiku
		t.SourceName = normalizeQuestionSourceName(t.SourceName)
		middleware.FillHash(t)
		err := dao.Tiku.Create(t)
		if err == nil {
			marshal, _ := json.Marshal(t)
			user, _ := c.Get("user")
			currentUser := user.(*entity.User)
			l := entity.Log{
				Qid:        t.ID,
				Action:     0,
				UserID:     currentUser.ID,
				Username:   currentUser.Username,
				CreateTime: time.Now(),
				OldAnswer:  string(marshal),
			}
			err := dao.Log.Create(&l)
			if err != nil {
				logger.SysError(err.Error())
			}
			count++
		}
	}
	// 新增了题目，缓存的搜题结果可能已过时。
	if count > 0 {
		invalidateSearchResultCaches("create_question")
	}
	c.JSON(200, gin.H{
		"message": "成功创建" + strconv.Itoa(count) + "条数据",
	})
}

// Courses 获取所有课程名称
func Courses(c *gin.Context) {
	// plat 已从题库模型移除（历史取值恒为 -1，无区分度），
	// 因此这里不再按平台过滤；为兼容旧前端仍接受该参数但忽略其值。
	tx := dao.Tiku.Select(dao.Tiku.CourseName)

	find, err := tx.Group(dao.Tiku.CourseName).Find()
	if err != nil {
		return
	}
	courses := make([]string, 0)
	for i := range find {
		if find[i].CourseName != "" {
			courses = append(courses, find[i].CourseName)
		}
	}
	c.JSON(200, courses)
}
