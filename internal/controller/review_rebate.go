package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	// 注册 PNG/JPEG 解码器：image.DecodeConfig 只认已注册的格式。
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 评价返现控制器。
//
// 权限边界：
//   - /user/review-rebate/* 只要 Auth，但**永远只用当前登录账户**（不接受 userId 参数），
//     否则就是一个「提交别人评价、看别人提交」的越权面；
//   - /admin/review-rebate/* 走 Auth + middleware.RequireAdmin，handler 内再用
//     requireAdmin 兜一次（路由漏挂中间件时也不会漏权限）。
//
// 上传安全（照 upload_security.go / user_exam_tool.go 的写法）：
//   - 只接受 PNG/JPEG，≤ 5 MB；
//   - **服务端实际解码**（image.DecodeConfig）验证是真图片，不信 Content-Type，
//     也不信扩展名 —— 「改名成 .png 的文本」必须 400；
//   - 落 data/review-rebate/<claim 相关私有目录>，**不是公开静态目录**，
//     也不返回任何可直连的 URL（ScreenshotPath 是 json:"-"）。

const (
	// maxReviewRebateScreenshotBytes 是单张截图上限（16-*.md §1.1：≤5 MB）。
	maxReviewRebateScreenshotBytes = int64(5 << 20)
	// reviewRebateScreenshotDirName 是私有落盘目录（相对工作目录）。
	reviewRebateScreenshotDirName = "review-rebate"
	// maxReviewRebateClaimedStars 是用户自填星数上界。
	maxReviewRebateClaimedStars = 5
)

// reviewRebateDB 返回当前数据库句柄；未初始化时返回 nil（调用方负责降级）。
//
// 与 referralDB 同构：单测里没有 manager 时不能 panic。
var reviewRebateDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

var errReviewRebateDBUnavailable = errors.New("数据库未初始化")

// requireReviewRebateDB 取一个带优先级的会话，并把「没有数据库」统一成一个错误。
func requireReviewRebateDB() (*gorm.DB, error) {
	db := reviewRebateDB()
	if db == nil {
		return nil, errReviewRebateDBUnavailable
	}
	return withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), nil
}

// 便于测试替换（与 referral.go 的注入风格一致）。
var (
	loadReviewRebateProgramView = func(userID int32) (service.ReviewRebateProgramView, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return service.ReviewRebateProgramView{}, err
		}
		return service.GetReviewRebateProgramViewWithDB(db, userID)
	}
	createReviewRebateClaim = func(input service.ReviewRebateClaimCreateInput) (entity.ReviewRebateClaim, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return entity.ReviewRebateClaim{}, err
		}
		return service.CreateReviewRebateClaimWithDB(db, input)
	}
	listReviewRebateClaims = func(filter service.ReviewRebateClaimListQuery, page int, pageSize int) (service.ReviewRebateClaimListResult, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return service.ReviewRebateClaimListResult{}, err
		}
		return service.ListReviewRebateClaimsWithDB(db, filter, page, pageSize)
	}
	scoreReviewRebateClaim = func(id int32) (entity.ReviewRebateClaim, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return entity.ReviewRebateClaim{}, err
		}
		return service.ScoreReviewRebateClaimWithDB(db, id, service.DefaultReviewRebateScorer)
	}
	decideReviewRebateClaim = func(reviewer entity.User, id int32, approve bool, note string) (entity.ReviewRebateClaim, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return entity.ReviewRebateClaim{}, err
		}
		return service.DecideReviewRebateClaimWithDB(db, reviewer, id, approve, note)
	}
	listReviewRebatePrograms = func() ([]entity.ReviewRebateProgram, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return nil, err
		}
		return service.ListReviewRebateProgramsWithDB(db)
	}
	updateReviewRebateProgram = func(id int32, update service.ReviewRebateProgramUpdate) (entity.ReviewRebateProgram, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return entity.ReviewRebateProgram{}, err
		}
		return service.UpdateReviewRebateProgramWithDB(db, id, update)
	}
	listReviewRebateRules = func(programID int32) ([]entity.ReviewRebateRule, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return nil, err
		}
		return service.ListReviewRebateRulesWithDB(db, programID)
	}
	createReviewRebateRule = func(rule entity.ReviewRebateRule) (entity.ReviewRebateRule, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return entity.ReviewRebateRule{}, err
		}
		return service.CreateReviewRebateRuleWithDB(db, rule)
	}
	updateReviewRebateRule = func(id int32, update service.ReviewRebateRuleUpdate) (entity.ReviewRebateRule, error) {
		db, err := requireReviewRebateDB()
		if err != nil {
			return entity.ReviewRebateRule{}, err
		}
		return service.UpdateReviewRebateRuleWithDB(db, id, update)
	}
	deleteReviewRebateRule = func(id int32) error {
		db, err := requireReviewRebateDB()
		if err != nil {
			return err
		}
		return service.DeleteReviewRebateRuleWithDB(db, id)
	}
)

// ReviewRebateProgram 返回当前活动、规则表（脱敏）与本人额度（用户端）。
func ReviewRebateProgram(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	view, err := loadReviewRebateProgramView(operator.ID)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// CreateReviewRebateClaim 处理截图提交（multipart，字段名 file）。
//
// 字段：
//   - file（必填）：PNG/JPEG，≤ 5 MB，服务端解码校验；
//   - claimedStars（可选）：用户自填 1–5；
//   - claimedReviewUrl（可选）：评价链接。
//
// 落盘：`data/review-rebate/<userID>/<sha256 前 2 位>/<sha256>.png|jpg`。
// 用内容摘要做文件名：同一张图重复上传只占一份磁盘，且文件名不含用户可见信息。
func CreateReviewRebateClaim(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请上传评价截图（multipart 字段名 file）"})
		return
	}
	defer func() {
		_ = form.RemoveAll()
	}()
	files := form.File["file"]
	if len(files) != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请上传且仅上传一张评价截图"})
		return
	}
	header := files[0]
	if header.Size <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "评价截图内容为空"})
		return
	}
	if header.Size > maxReviewRebateScreenshotBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "评价截图不能超过 5 MB"})
		return
	}

	data, err := readReviewRebateScreenshot(header)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	// 服务端解码：**不信 Content-Type，也不信扩展名**。
	// 「改名成 .png 的文本」在这一步被拒（image.DecodeConfig 只认真实的格式头）。
	extension, err := reviewRebateScreenshotExtension(data)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}

	claimedStars, err := parseReviewRebateClaimedStars(c.PostForm("claimedStars"))
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}

	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])
	relativePath, err := storeReviewRebateScreenshot(operator.ID, digestHex, extension, data)
	if err != nil {
		logger.SysError("评价返现截图落盘失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "评价截图保存失败，请稍后重试"})
		return
	}

	program, err := loadActiveReviewRebateProgram()
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}

	claim, err := createReviewRebateClaim(service.ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           operator.ID,
		ScreenshotPath:   relativePath,
		ScreenshotSHA256: digestHex,
		ClaimedStars:     claimedStars,
		ClaimedReviewURL: strings.TrimSpace(c.PostForm("claimedReviewUrl")),
	})
	if err != nil {
		// 落盘成功但落账失败（重复/超限）：把刚写的文件删掉，避免私有目录被
		// 「反复重试的同一张图」灌满。文件是按摘要命名的，删掉不影响其他行。
		removeReviewRebateScreenshot(relativePath)
		writeReviewRebateError(c, err)
		return
	}

	recordAccountAudit(c, &operator, "review_rebate", 1, "提交评价返现", map[string]any{
		"claimId":   claim.ID,
		"programId": claim.ProgramID,
		"status":    claim.Status,
	})

	c.JSON(http.StatusCreated, gin.H{
		"id":        claim.ID,
		"programId": claim.ProgramID,
		"status":    claim.Status,
		// 明确告知边界：不声称能识别「这条评论就是这个人写的」。
		"authorshipVerified": false,
		"notice":             "已收到截图。系统只核验公开评价是否存在且内容一致，不做作者归属断言；结果由人工复核。",
	})
}

// MyReviewRebateClaims 返回本人的提交列表（分页）。
func MyReviewRebateClaims(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	page, pageSize, ok := reviewRebatePagination(c)
	if !ok {
		return
	}
	result, err := listReviewRebateClaims(service.ReviewRebateClaimListQuery{
		UserID: operator.ID,
		Status: strings.TrimSpace(c.Query("status")),
	}, page, pageSize)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// AdminReviewRebatePrograms 返回全部活动（管理员）。
func AdminReviewRebatePrograms(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	programs, err := listReviewRebatePrograms()
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": programs})
}

// AdminUpdateReviewRebateProgram 部分更新活动配置（管理员）。
//
// 指针语义：请求体里没有出现的字段 = 保持现值（与系统设置的约定一致）。
func AdminUpdateReviewRebateProgram(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseReviewRebateID(c)
	if !ok {
		return
	}
	var input struct {
		Name               *string  `json:"name"`
		StoreURL           *string  `json:"storeUrl"`
		ExtensionID        *string  `json:"extensionId"`
		Enabled            *bool    `json:"enabled"`
		MaxClaimsPerUser   *int     `json:"maxClaimsPerUser"`
		MaxAmountPerUser   *float64 `json:"maxAmountPerUser"`
		MinAgentConfidence *float64 `json:"minAgentConfidence"`
		TutorialVersionID  *int32   `json:"tutorialVersionId"`
		PolicyVersion      *string  `json:"policyVersion"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	program, err := updateReviewRebateProgram(id, service.ReviewRebateProgramUpdate{
		Name:               input.Name,
		StoreURL:           input.StoreURL,
		ExtensionID:        input.ExtensionID,
		Enabled:            input.Enabled,
		MaxClaimsPerUser:   input.MaxClaimsPerUser,
		MaxAmountPerUser:   input.MaxAmountPerUser,
		MinAgentConfidence: input.MinAgentConfidence,
		TutorialVersionID:  input.TutorialVersionID,
		PolicyVersion:      input.PolicyVersion,
	})
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	recordAccountAudit(c, &operator, "review_rebate", 2, "更新评价返现活动", map[string]any{
		"programId": program.ID,
		"enabled":   program.Enabled,
	})
	c.JSON(http.StatusOK, program)
}

// AdminReviewRebateRules 返回规则表（管理员，programId 可选筛选）。
func AdminReviewRebateRules(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	programID, err := parseOptionalPositiveInt32(c.Query("programId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数格式无效"})
		return
	}
	rules, err := listReviewRebateRules(programID)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rules})
}

// AdminCreateReviewRebateRule 新建规则（管理员）。
func AdminCreateReviewRebateRule(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input struct {
		ProgramID       int32   `json:"programId"`
		MinStars        int     `json:"minStars"`
		MaxStars        int     `json:"maxStars"`
		MinContentScore int     `json:"minContentScore"`
		MaxContentScore int     `json:"maxContentScore"`
		Amount          float64 `json:"amount"`
		Priority        int     `json:"priority"`
		Enabled         *bool   `json:"enabled"`
		Remark          string  `json:"remark"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	// 缺省 enabled = true：新建规则不写 enabled 时应该是「立刻生效」，
	// 否则运营配完发现不生效还得回头改一次。
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	rule, err := createReviewRebateRule(entity.ReviewRebateRule{
		ProgramID:       input.ProgramID,
		MinStars:        input.MinStars,
		MaxStars:        input.MaxStars,
		MinContentScore: input.MinContentScore,
		MaxContentScore: input.MaxContentScore,
		Amount:          input.Amount,
		Priority:        input.Priority,
		Enabled:         enabled,
		Remark:          input.Remark,
	})
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	recordAccountAudit(c, &operator, "review_rebate", 3, "新建评价返现规则", map[string]any{
		"ruleId": rule.ID,
		"amount": rule.Amount,
	})
	c.JSON(http.StatusCreated, rule)
}

// AdminUpdateReviewRebateRule 部分更新规则（管理员）。
func AdminUpdateReviewRebateRule(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	id, ok := parseReviewRebateID(c)
	if !ok {
		return
	}
	var input struct {
		ProgramID       *int32   `json:"programId"`
		MinStars        *int     `json:"minStars"`
		MaxStars        *int     `json:"maxStars"`
		MinContentScore *int     `json:"minContentScore"`
		MaxContentScore *int     `json:"maxContentScore"`
		Amount          *float64 `json:"amount"`
		Priority        *int     `json:"priority"`
		Enabled         *bool    `json:"enabled"`
		Remark          *string  `json:"remark"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	rule, err := updateReviewRebateRule(id, service.ReviewRebateRuleUpdate{
		ProgramID:       input.ProgramID,
		MinStars:        input.MinStars,
		MaxStars:        input.MaxStars,
		MinContentScore: input.MinContentScore,
		MaxContentScore: input.MaxContentScore,
		Amount:          input.Amount,
		Priority:        input.Priority,
		Enabled:         input.Enabled,
		Remark:          input.Remark,
	})
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, rule)
}

// AdminDeleteReviewRebateRule 删除规则（管理员）。
func AdminDeleteReviewRebateRule(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	id, ok := parseReviewRebateID(c)
	if !ok {
		return
	}
	if err := deleteReviewRebateRule(id); err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// AdminReviewRebateClaims 返回待审/全部提交单（管理员，分页 + 筛选）。
func AdminReviewRebateClaims(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	page, pageSize, ok := reviewRebatePagination(c)
	if !ok {
		return
	}
	programID, err := parseOptionalPositiveInt32(c.Query("programId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数格式无效"})
		return
	}
	userID, err := parseOptionalPositiveInt32(c.Query("userId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数格式无效"})
		return
	}
	result, err := listReviewRebateClaims(service.ReviewRebateClaimListQuery{
		ProgramID: programID,
		UserID:    userID,
		Status:    strings.TrimSpace(c.Query("status")),
	}, page, pageSize)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// AdminDecideReviewRebateClaim 批准/拒绝一条提交单（管理员）。
//
// 请求体：{"action":"approve"|"reject","note":"..."}
// 批准时在**同一事务**内写 award + 累加钱包 + 写账单；claim_id 唯一索引防重复发放。
func AdminDecideReviewRebateClaim(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseReviewRebateID(c)
	if !ok {
		return
	}
	var input struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "approve" && action != "reject" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "action 只能是 approve 或 reject"})
		return
	}
	claim, err := decideReviewRebateClaim(operator, id, action == "approve", input.Note)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	recordAccountAudit(c, &operator, "review_rebate", 4, "复核评价返现", map[string]any{
		"claimId": claim.ID,
		"action":  action,
		"status":  claim.Status,
		"amount":  claim.AwardAmount,
	})
	c.JSON(http.StatusOK, claim)
}

// AdminScoreReviewRebateClaim 触发一次 Agent 打分（管理员手工重跑用）。
//
// 幂等：已批准/已拒绝的行会被服务层拒绝（ErrReviewRebateClaimState），
// 因为改一个已入账单子的金额会让账更难对。
func AdminScoreReviewRebateClaim(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	id, ok := parseReviewRebateID(c)
	if !ok {
		return
	}
	claim, err := scoreReviewRebateClaim(id)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, claim)
}

// loadActiveReviewRebateProgram 取当前生效活动（提交时用来填 ProgramID）。
func loadActiveReviewRebateProgram() (entity.ReviewRebateProgram, error) {
	db, err := requireReviewRebateDB()
	if err != nil {
		return entity.ReviewRebateProgram{}, err
	}
	return service.GetReviewRebateProgramWithDB(db)
}

// readReviewRebateScreenshot 读取上传内容，同时把大小上限作为硬闸再核一次。
func readReviewRebateScreenshot(header *multipart.FileHeader) ([]byte, error) {
	source, err := header.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: 上传文件无法读取", service.ErrReviewRebateInvalidInput)
	}
	defer source.Close()
	data, err := io.ReadAll(io.LimitReader(source, maxReviewRebateScreenshotBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 上传文件读取失败", service.ErrReviewRebateInvalidInput)
	}
	if int64(len(data)) > maxReviewRebateScreenshotBytes {
		return nil, service.ErrReviewRebateScreenshotTooLarge
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: 上传文件为空", service.ErrReviewRebateInvalidInput)
	}
	return data, nil
}

// reviewRebateScreenshotExtension 用 image.DecodeConfig 真的解一次图，返回落盘扩展名。
//
// 只接受 PNG/JPEG：其它格式（GIF/WebP/SVG）一律拒绝。
// SVG 尤其不能收 —— 它是可执行文档，任何「当图片存起来再回显」的路径都是 XSS 面。
func reviewRebateScreenshotExtension(data []byte) (string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", service.ErrReviewRebateScreenshotInvalid
	}
	if config.Width <= 0 || config.Height <= 0 {
		return "", service.ErrReviewRebateScreenshotInvalid
	}
	switch strings.ToLower(format) {
	case "png":
		return "png", nil
	case "jpeg":
		return "jpg", nil
	default:
		return "", service.ErrReviewRebateScreenshotInvalid
	}
}

// storeReviewRebateScreenshot 把截图落到私有目录并返回相对路径。
//
// 目录布局：data/review-rebate/<userID>/<sha256[:2]>/<sha256>.<ext>
//   - 按用户分目录：管理员排查时不必在一堆哈希里翻；
//   - 按摘要前 2 位分流：单目录文件数上限从「全站」降到「1/256 全站」；
//   - 文件名 = 内容摘要：重复上传天然幂等（同一张图只占一份磁盘）。
//
// 权限：目录 0700、文件 0600 —— 截图里有用户账号信息，不能让同机其他进程顺手读到。
func storeReviewRebateScreenshot(userID int32, digestHex string, extension string, data []byte) (string, error) {
	if userID <= 0 || len(digestHex) < 4 {
		return "", errors.New("评价返现截图参数无效")
	}
	relativeDir := filepath.Join(reviewRebateScreenshotDirName,
		strconv.FormatInt(int64(userID), 10), digestHex[:2])
	absoluteDir := filepath.Join("data", relativeDir)
	if err := os.MkdirAll(absoluteDir, 0o700); err != nil {
		return "", err
	}
	filename := digestHex + "." + extension
	absolutePath := filepath.Join(absoluteDir, filename)
	// 已存在就直接复用：内容相同的图没必要重写，也避免并发写的半个文件。
	if _, statErr := os.Stat(absolutePath); statErr == nil {
		return filepath.Join(relativeDir, filename), nil
	}
	temporary, err := os.CreateTemp(absoluteDir, ".upload-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer func() {
		// 正常路径已经在下面 Close；这里只保证异常路径不留残片。
		_ = os.Remove(temporaryPath)
	}()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, absolutePath); err != nil {
		return "", err
	}
	return filepath.Join(relativeDir, filename), nil
}

// removeReviewRebateScreenshot 删除一张截图（落账失败时的补偿）。
//
// 失败只记日志：文件残留是**可用性可接受**的降级（最多占磁盘），
// 清理失败不该让用户看到 500。
func removeReviewRebateScreenshot(relativePath string) {
	trimmed := strings.TrimSpace(relativePath)
	if trimmed == "" {
		return
	}
	// 只允许删 data/review-rebate 下的东西：拼接前先把 .. 挡掉，
	// 避免任何形式的路径穿透（这里虽然是内部生成的路径，仍按不可信输入处理）。
	cleaned := filepath.Clean(trimmed)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return
	}
	if !strings.HasPrefix(cleaned, reviewRebateScreenshotDirName+string(filepath.Separator)) {
		return
	}
	if err := os.Remove(filepath.Join("data", cleaned)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.SysError("评价返现截图清理失败: " + logger.RedactSecrets(err.Error()))
	}
}

// reviewRebatePagination 解析分页参数。
func reviewRebatePagination(c *gin.Context) (int, int, bool) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return page, pageSize, true
}

// parseReviewRebateID 解析 :id 路径参数。
func parseReviewRebateID(c *gin.Context) (int32, bool) {
	value, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || value <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数 id 无效"})
		return 0, false
	}
	return int32(value), true
}

// parseReviewRebateClaimedStars 解析用户自填星数：空 = 未填（0）。
func parseReviewRebateClaimedStars(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%w: 星数必须是整数", service.ErrReviewRebateInvalidInput)
	}
	if value < 0 || value > maxReviewRebateClaimedStars {
		return 0, fmt.Errorf("%w: 星数必须在 0–%d", service.ErrReviewRebateInvalidInput, maxReviewRebateClaimedStars)
	}
	return value, nil
}

// writeReviewRebateError 把服务层哨兵错误翻成 HTTP 状态码。
func writeReviewRebateError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "操作评价返现失败"
	switch {
	case errors.Is(err, service.ErrReviewRebateDisabled):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrReviewRebateSchemaUnavailable):
		status = http.StatusServiceUnavailable
		message = "评价返现暂不可用，请稍后再试"
	case errors.Is(err, service.ErrReviewRebateProgramNotFound), errors.Is(err, service.ErrReviewRebateClaimNotFound),
		errors.Is(err, service.ErrReviewRebateRuleNotFound):
		// 活动不存在对用户来说等于「没开」，用 404 更准确。
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, service.ErrReviewRebateDuplicate):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrReviewRebateLimitReached):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrReviewRebateClaimState):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrReviewRebateNoRuleMatched):
		status = http.StatusOK
		message = err.Error()
	case errors.Is(err, service.ErrReviewRebateScreenshotTooLarge):
		status = http.StatusRequestEntityTooLarge
		message = "评价截图不能超过 5 MB"
	case errors.Is(err, service.ErrReviewRebateScreenshotInvalid):
		status = http.StatusBadRequest
		message = "只接受真实的 PNG/JPEG 图片（服务端已解码校验）"
	case errors.Is(err, service.ErrReviewRebateInvalidInput),
		errors.Is(err, service.ErrReviewRebateReviewerInvalid),
		errors.Is(err, service.ErrInvalidReviewRebateAmount),
		errors.Is(err, service.ErrInvalidReviewRebateConfidence),
		errors.Is(err, service.ErrInvalidReviewRebateMaxClaims):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, errReviewRebateDBUnavailable):
		status = http.StatusServiceUnavailable
		message = "服务暂不可用"
	case errors.Is(err, gorm.ErrRecordNotFound):
		status = http.StatusNotFound
		message = service.ErrReviewRebateClaimNotFound.Error()
	}
	if status >= http.StatusInternalServerError {
		logger.SysError("评价返现操作失败: " + logger.RedactSecrets(err.Error()))
	}
	c.JSON(status, gin.H{"message": message})
}
