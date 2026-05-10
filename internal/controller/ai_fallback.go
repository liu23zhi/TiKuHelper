package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

type aiFallbackInput struct {
	Question string   `json:"question"`
	Type     int      `json:"type"`
	Options  []string `json:"options"`
}

var runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
	return service.SearchAIFallbackWithDB(manager.GetManager().GetDB(), model.SearchRequest{
		Question: strings.TrimSpace(question),
		Type:     questionType,
		Options:  options,
	})
}

func AIFallback(c *gin.Context) {
	var input aiFallbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusOK, service.BuildAIFallbackInvalidInputResult("参数错误"))
		return
	}

	if strings.TrimSpace(input.Question) == "" {
		c.JSON(http.StatusOK, service.BuildAIFallbackInvalidInputResult("题目不能为空"))
		return
	}

	c.JSON(http.StatusOK, runAIFallbackService(input.Question, input.Type, input.Options))
}
