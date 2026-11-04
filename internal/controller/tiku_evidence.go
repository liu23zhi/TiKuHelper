package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

var tikuAnswerEvidenceDB = func() *gorm.DB { return manager.GetManager().GetDB() }

func UploadTikuAnswerEvidence(c *gin.Context) {
	var input service.TikuAnswerEvidenceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	record, created, err := service.SaveTikuAnswerEvidenceWithDB(tikuAnswerEvidenceDB(), input)
	if err != nil {
		if errors.Is(err, service.ErrInvalidTikuAnswerEvidence) {
			c.JSON(http.StatusBadRequest, gin.H{"message": strings.TrimPrefix(err.Error(), service.ErrInvalidTikuAnswerEvidence.Error()+": ")})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "题库证据保存失败"})
		}
		return
	}
	if created {
		invalidateSearchResultCaches("tiku_answer_evidence_created")
	}
	c.JSON(http.StatusOK, gin.H{"id": record.ID, "created": created, "status": record.Status})
}
