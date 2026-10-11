package model

// SearchRequest 搜题的请求体
//
// 注意：不再有 plat 字段。它历史取值恒为 -1、没有任何区分度，已彻底移除。
// 旧客户端若仍发送 "plat": -1，会被 JSON 解码**静默忽略**（本项目未启用
// DisallowUnknownFields），不会报错。
type SearchRequest struct {
	QID       string   `json:"qid"`
	Question  string   `json:"question"`
	Options   []string `json:"options"`
	Type      int      `json:"type"`
	SkipCache *bool    `json:"skipCache,omitempty"`

	CourseName string `json:"courseName"`

	Extra string `json:"extra"`
}

// SearchResponse 搜题响应体
type SearchResponse struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Type     int      `json:"type"`
	Answer   Answer   `json:"answer"` // 最相似，最可能的答案->即moreAnswer中出现最多的答案

	// Warnings 是面向调用方的非阻断提示，例如「AI 正在使用未配置价格的模型」。
	// 只在确有需要提醒的情况出现，正常响应不会带该字段，避免影响既有客户端。
	Warnings []string `json:"warnings,omitempty"`
}

// Answer 答案 该实体只会增加而不会减少
type Answer struct {
	AnswerKey     []string `json:"answerKey"`     // 如果传了选项可以格式化为ABCD
	AnswerKeyText string   `json:"answerKeyText"` // 答案ABCD文本 分隔符默认是 #

	AnswerIndex []int  `json:"answerIndex"` // 如果用户传了options 将会自动帮用户计算出答案的角标，依据Answer来计算的
	AnswerText  string `json:"answerText"`  // 答案文本 分隔符默认是 #

	BestAnswer []string   `json:"bestAnswer"` // 最有可能的答案
	AllAnswer  [][]string `json:"allAnswer"`  // 所有接口聚合的答案
}

// Question 最简单的通用问题答案模型
type Question struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Type     uint     `json:"type"`
	Answer   []string `json:"answer"`
}
