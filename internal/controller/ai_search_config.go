package controller

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

type aiSearchTestInput struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
}

var runAISearchDebug = func(input aiSearchTestInput) ([][]string, internalsearch.AISearchDebugResult, error) {
	query := url.Values{}
	if strings.TrimSpace(input.BaseURL) != "" {
		query.Set("url", strings.TrimSpace(input.BaseURL))
	}
	if strings.TrimSpace(input.APIKey) != "" {
		query.Set("key", strings.TrimSpace(input.APIKey))
	}
	if strings.TrimSpace(input.Model) != "" {
		query.Set("model", strings.TrimSpace(input.Model))
	}

	resolved, err := service.BuildAISearchRequestConfigWithDB(manager.GetManager().GetDB(), query, true)
	if err != nil {
		return nil, internalsearch.AISearchDebugResult{}, err
	}
	client := &internalsearch.ChatGptClient{
		Enable:      true,
		APIKey:      resolved.Config.APIKey,
		BaseURL:     resolved.Config.BaseURL,
		Model:       resolved.Config.Model,
		Prompt:      strings.TrimSpace(input.Prompt),
		Temperature: 0,
		TopP:        0,
	}
	if strings.TrimSpace(client.Prompt) == "" {
		client.Prompt = resolved.Config.Prompt
	}
	return client.SearchAnswerDebug(model.SearchRequest{Question: "hello"})
}

func AISearchConfigDetail(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	config, err := service.GetAISearchConfigWithDB(manager.GetManager().GetDB())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取 AI 搜索配置失败",
		})
		return
	}

	c.JSON(http.StatusOK, config)
}

func AISearchModels(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	models, err := service.ListAISearchModelsWithDB(manager.GetManager().GetDB(), c.Request.URL.Query())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取 AI 模型列表失败",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"models": models,
	})
}

func TestAISearchConfig(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限测试 AI 搜索配置"})
		return
	}

	var input aiSearchTestInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	answers, debug, err := runAISearchDebug(input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "AI 搜索测试失败"})
		return
	}

	flattened := make([]string, 0)
	for _, group := range answers {
		flattened = append(flattened, group...)
	}

	c.JSON(http.StatusOK, gin.H{
		"answers":     flattened,
		"endpoint":    debug.Endpoint,
		"requestBody": debug.RequestBody,
		"rawResponse": debug.RawResponse,
		"usage":       debug.Usage,
	})
}

func UpdateAISearchConfig(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input service.UpdateAISearchConfigInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	config, err := service.UpdateAISearchConfigWithDB(manager.GetManager().GetDB(), input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "更新 AI 搜索配置失败",
		})
		return
	}

	c.JSON(http.StatusOK, config)
}
