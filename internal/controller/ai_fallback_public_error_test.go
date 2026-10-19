package controller

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/service"
)

func TestAIFallbackTaskResponseHidesUpstreamMessageWithoutChangingPrivateResult(t *testing.T) {
	detail := `HTTP 400: {"error":{"message":"model requires Responses; account only supports Chat Completions"}}`
	privateError := &service.AIFallbackError{Code: service.AIFallbackErrorCodeUpstreamError, Message: detail}
	task := aiFallbackTask{
		Status: aiFallbackTaskStatusFailed,
		Result: service.AIFallbackResult{Error: privateError, FailureDetail: detail},
	}
	response := buildAIFallbackTaskResponse(task)
	if response.Error == nil || response.Error.Code != service.AIFallbackErrorCodeUpstreamError || response.Error.Message != "服务器内部错误，搜题失败。" {
		t.Fatalf("task status leaked internal upstream error: %+v", response.Error)
	}
	if privateError.Message != detail || task.Result.FailureDetail != detail {
		t.Fatal("public error sanitization changed private audit details")
	}
}
