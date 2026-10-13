package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/internal/uapi"
)

const uapiSearchControllerTimeout = 60 * time.Second

var newUAPISearchClient = func(key string) *uapi.Client {
	return uapi.NewClient(key).SetTimeout(uapiSearchControllerTimeout)
}

// UAPISearchEngines 返回 UAPI 搜索引擎配置，仅管理员可用。
func UAPISearchEngines(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	key := service.GetUAPIAPIKeyWithDB(resolveSearchPersistenceDB())
	if !requireUAPISearchToken(c, key) {
		return
	}
	client := newUAPISearchClient(key)
	ctx, cancel := context.WithTimeout(c.Request.Context(), uapiSearchControllerTimeout)
	defer cancel()
	result, err := client.GetSearchEngines(ctx)
	if err != nil {
		writeUAPISearchError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// UAPISearchAggregate 执行 UAPI 实时网页搜索，仅管理员可用。
func UAPISearchAggregate(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var request uapi.SearchAggregateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "INVALID_REQUEST", "message": "请求参数格式错误"})
		return
	}
	key := service.GetUAPIAPIKeyWithDB(resolveSearchPersistenceDB())
	if !requireUAPISearchToken(c, key) {
		return
	}
	client := newUAPISearchClient(key).SetMaxRetries(1)
	ctx, cancel := context.WithTimeout(c.Request.Context(), uapiSearchControllerTimeout)
	defer cancel()
	result, err := client.SearchAggregate(ctx, request)
	if err != nil {
		writeUAPISearchError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func requireUAPISearchToken(c *gin.Context, key string) bool {
	if strings.TrimSpace(key) != "" {
		return true
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"code":    "UAPI_TOKEN_MISSING",
		"message": "UApiPro 令牌为空，请先在系统设置中配置",
	})
	return false
}

func writeUAPISearchError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	code := "UPSTREAM_ERROR"
	message := "搜索服务请求失败"
	var apiErr *uapi.APIError
	if errors.As(err, &apiErr) {
		status = apiErr.HTTPStatus
		code = strings.TrimSpace(apiErr.Code)
		message = apiErr.Message
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			status = http.StatusBadGateway
			code = "UPSTREAM_AUTH_ERROR"
			message = "UApiPro 令牌鉴权失败，请检查系统设置"
		}
		if code == "" {
			code = "UPSTREAM_ERROR"
		}
		if message == "" {
			message = apiErr.Error()
		}
	} else if errors.Is(err, uapi.ErrInvalidArgument) {
		status, code, message = http.StatusBadRequest, "INVALID_REQUEST", err.Error()
	} else if errors.Is(err, uapi.ErrUnauthorized) {
		status, code, message = http.StatusBadGateway, "UPSTREAM_AUTH_ERROR", "UApiPro 令牌鉴权失败，请检查系统设置"
	} else if errors.Is(err, uapi.ErrTimeout) {
		status, code, message = http.StatusGatewayTimeout, "TIMEOUT", "搜索服务请求超时"
	} else if errors.Is(err, uapi.ErrNetwork) {
		message = "搜索服务网络请求失败"
	}
	c.JSON(status, gin.H{"code": code, "message": message})
}
