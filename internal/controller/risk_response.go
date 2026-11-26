package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func riskResponseAdmin(c *gin.Context) (*gorm.DB, int32, bool) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return nil, 0, false
	}
	actor, _ := currentOperator(c)
	db, err := requireRiskAdminDB(c)
	if err != nil {
		writeRiskAdminError(c, err, "读取风控处置失败")
		return nil, 0, false
	}
	return db, actor.ID, true
}
func writeResponseResult(c *gin.Context, data any, err error) {
	if errors.Is(err, service.ErrLegacyRiskResponsePolicyReadOnly) {
		c.JSON(http.StatusConflict, gin.H{"message": err.Error()})
		return
	}
	if errors.Is(err, service.ErrRiskResponseInvalid) {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if err != nil {
		writeRiskAdminError(c, err, "风控处置操作失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": data})
}
func bindResponseBody(c *gin.Context, dst any) bool {
	// Only accept decision fields. In particular, an IP, CIDR, actor or city may never be supplied by the caller.
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.Decode(new(any)) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体无效或包含未支持的字段"})
		return false
	}
	return true
}

func RiskResponsePolicyGet(c *gin.Context) {
	db, _, ok := riskResponseAdmin(c)
	if !ok {
		return
	}
	p, err := service.GetRiskResponsePolicy(db, c.Param("detector"))
	writeResponseResult(c, p, err)
}
func RiskResponsePolicyPut(c *gin.Context) {
	db, actor, ok := riskResponseAdmin(c)
	if !ok {
		return
	}
	var body struct {
		Mode            string `json:"mode"`
		DurationMinutes int    `json:"durationMinutes"`
	}
	if !bindResponseBody(c, &body) {
		return
	}
	p, err := service.PutRiskResponsePolicy(db, c.Param("detector"), body.Mode, body.DurationMinutes, actor)
	writeResponseResult(c, p, err)
}
func responseWarningID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "告警编号无效"})
		return 0, false
	}
	return id, true
}
func RiskAlertResponseGet(c *gin.Context) {
	db, _, ok := riskResponseAdmin(c)
	if !ok {
		return
	}
	id, ok := responseWarningID(c)
	if !ok {
		return
	}
	v, err := service.GetRiskAlertResponse(db, id)
	writeResponseResult(c, v, err)
}
func RiskAlertResponseApply(c *gin.Context) {
	db, actor, ok := riskResponseAdmin(c)
	if !ok {
		return
	}
	id, ok := responseWarningID(c)
	if !ok {
		return
	}
	var body struct {
		Action          string `json:"action"`
		DurationMinutes *int   `json:"durationMinutes"`
		Reason          string `json:"reason"`
		Confirmed       bool   `json:"confirmed"`
		ResponseID      int64  `json:"responseId"`
		ExpectedTarget  string `json:"expectedTarget"`
	}
	if !bindResponseBody(c, &body) {
		return
	}
	if body.Action == "revoke_auto" {
		if !body.Confirmed || body.ResponseID <= 0 || body.DurationMinutes != nil || body.ExpectedTarget != "" || body.Reason != "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "撤销必须确认并仅提供自动处置记录编号"})
			return
		}
		v, err := service.RevokeAutomaticRiskResponse(db, id, body.ResponseID, actor)
		writeResponseResult(c, v, err)
		return
	}
	if body.DurationMinutes == nil || body.ResponseID != 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "必须明确提供 durationMinutes（0 表示永久）"})
		return
	}
	v, err := service.ApplyRiskAlertResponse(db, id, actor, service.RiskResponseInput{Action: body.Action, DurationMinutes: *body.DurationMinutes, Reason: body.Reason, Confirmed: body.Confirmed, ExpectedTarget: body.ExpectedTarget})
	writeResponseResult(c, v, err)
}
