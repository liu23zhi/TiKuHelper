package errors

import "errors"

const PublicSearchFailureMessage = "服务器内部错误，搜题失败。"

var (
	// ErrTargetAPIFlow 对方API接口限流
	ErrTargetAPIFlow = errors.New("对方API接口限流")
	// ErrTargetServerError 上游服务器异常；保留变量名以兼容现有 errors.Is 调用。
	ErrTargetServerError = errors.New("上游服务器异常")
	// ErrTargetNoAnswer 对方没有答案
	ErrTargetNoAnswer = errors.New("对方没有答案")
	// ErrParserJSON 解析json错误
	ErrParserJSON = errors.New("解析json错误")
	// ErrTokenRequired 需要提供卡密
	ErrTokenRequired = errors.New("需要提供卡密")
)
