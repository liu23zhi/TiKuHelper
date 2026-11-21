package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func AdminReviewRebateVerification(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	id, ok := parseReviewRebateID(c)
	if !ok {
		return
	}
	db, err := requireReviewRebateDB()
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	db = db.WithContext(c.Request.Context())
	if _, err := service.GetReviewRebateClaimWithDB(db, id); err != nil {
		writeReviewRebateError(c, err)
		return
	}
	var latest *entity.ReviewRebateVerification
	var check entity.ReviewRebateVerification
	if err := db.Where("claim_id = ?", id).Order("id DESC").Take(&check).Error; err == nil {
		latest = &check
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		writeReviewRebateError(c, err)
		return
	}
	var reversal *entity.ReviewRebateClawback
	var row entity.ReviewRebateClawback
	if err := db.Where("claim_id = ?", id).Take(&row).Error; err == nil {
		reversal = &row
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"verification": latest, "clawback": reversal})
}

func AdminRecordReviewRebateVerification(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	admin, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseReviewRebateID(c)
	if !ok {
		return
	}
	var input struct {
		Result   string `json:"result"`
		Reason   string `json:"reason"`
		Evidence string `json:"evidence"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	db, err := requireReviewRebateDB()
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	row, err := service.RecordReviewRebateVerificationWithDB(db.WithContext(c.Request.Context()), admin.ID, id, input.Result, input.Reason, input.Evidence)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusCreated, row)
}

func AdminClawBackReviewRebate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	admin, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseReviewRebateID(c)
	if !ok {
		return
	}
	var input struct {
		VerificationID int32 `json:"verificationId"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	db, err := requireReviewRebateDB()
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	row, err := service.ClawBackReviewRebateWithDB(db.WithContext(c.Request.Context()), admin.ID, id, input.VerificationID)
	if err != nil {
		writeReviewRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, row)
}
