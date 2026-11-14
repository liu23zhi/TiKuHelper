package controller

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
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

		// 「归档 = 不存在」的口径同样适用于导入（Q77-1 影响面 2）：
		// 已归档的同键题（hash + course_name）不会再出现在上面的 existing 判定里，
		// 但它仍占着唯一索引 hash_course_idx。这里先复活该行，
		// 避免直接 Create 撞唯一键把整个导入打成 500。
		if revived, reviveErr := reviveArchivedTikuForImport(item); reviveErr != nil {
			return result, reviveErr
		} else if revived {
			result.Created++
			continue
		}

		// 「已存在同键题（hash + course_name）、未归档、但没带 id」必须先按 skipped 处理。
		//
		// 上面的 existing 判定只看 `item.ID > 0`，所以「id=0 但题库里已有同一道题」
		// 这条路径完全没有被覆盖 —— 直接 Create 会撞唯一索引 hash_course_idx，
		// 进而被 ImportQuestions 渲染成 500「服务器错误」。实测复现：
		//
		//	第一次导入 {"id":0,"question":"探针题目",...}  → 200 created=1
		//	第二次导入 同题干同课程、id=0、答案不同        → 500
		//	  constraint failed: UNIQUE constraint failed: tiku.hash, tiku.course_name
		//
		// 而 CSV 路径（service/import_data.go 的 importQuestionRows）在同样输入下
		// 返回 skipped（reason「已存在相同题目（hash + course_name）」）。
		// 两条路径必须同口径：同一批数据走 JSON 与走 CSV，created/updated/skipped
		// 三个计数必须完全相同，否则调用方无法在两种导入方式之间迁移。
		//
		// 注意这里**不能**直接复用 overwrite 分支：overwrite 的语义是「按 id 覆盖
		// 指定的那一行」，而这条路径连 id 都没有，没有「覆盖哪一行」的定义。
		// 与 CSV 保持一致 = skipped（107 Q13c：跳过 + 报告，批量导入默认不改历史答案）。
		var sameKeyCount int64
		if err := dao.Tiku.UnderlyingDB().Model(&entity.Tiku{}).
			Where("hash = ? AND course_name = ? AND deleted_at IS NULL", item.Hash, item.CourseName).
			Count(&sameKeyCount).Error; err != nil {
			return result, err
		}
		if sameKeyCount > 0 {
			result.Skipped++
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

	tx := dao.Tiku.WithContext(dbqueue.WithPriority(databaseRequestContext(c), dbqueue.PriorityLow)).Order(dao.Tiku.ID.Desc())
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

// DeleteQuestion 删除题目 —— 自本轮起改为**归档（软删除）**，不再物理删除。
//
// 口径（Q77-1，77-*.md §11.3）：
//   - deleted_at 置为非 NULL，题目在所有业务路径上「等于不存在」（搜题/导入/导出/统计）；
//   - archived_reason / archived_at / archived_by_user_id 记录归档来源、时点与操作者；
//   - 同一事务内先写 logs（Action = TikuActionArchive = 59，OldAnswer 存完整题库快照）
//     再置归档标记，并在提交后补写 AccountAuditEvent；
//   - 删除原因强制填（§11.8）：Query 参数 reason 去空格后长度 ≥ 2，否则 400。
//
// 恢复见 RestoreArchivedTiku，归档查看见 ListArchivedTikus。
func DeleteQuestion(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	reason := c.Query("reason")
	if archivedReasonRuneLength(reason) < archiveReasonMinLength {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "请填写删除原因（至少 2 个字符）",
		})
		return
	}
	reason = strings.TrimSpace(reason)

	user, _ := c.Get("user")
	currentUser, _ := user.(*entity.User)
	if currentUser == nil || currentUser.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message": "未登录",
		})
		return
	}

	archived, err := archiveTikuQuestion(currentUser, int32(id), reason)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"message": "题目不存在或已归档",
			})
			return
		}
		logger.SysError("归档题库失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "服务器错误",
		})
		return
	}

	// 双通道审计：logs 已随事务提交，这里补账户审计流（§3.2.1）。
	recordAccountAudit(c, currentUser, "tiku", TikuActionArchive, "归档题库："+reason, map[string]any{
		"tikuId": archived.ID,
		"reason": reason,
	})

	// 题目被归档，之前基于它建立的搜题结果缓存必须作废。
	invalidateSearchResultCaches("delete_question")
	c.JSON(http.StatusOK, gin.H{
		"message": "已归档",
		"id":      archived.ID,
		"reason":  reason,
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
	tx := dao.Tiku.WithContext(dbqueue.WithPriority(databaseRequestContext(c), dbqueue.PriorityLow)).Select(dao.Tiku.CourseName)

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
