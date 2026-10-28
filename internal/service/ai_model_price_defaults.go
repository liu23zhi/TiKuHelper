package service

import "github.com/itihey/tikuAdapter/internal/entity"

// Only verified model metadata belongs here. Unknown or gateway-specific
// context windows remain unset rather than being guessed from a model name.
func buildDefaultAIModelPrices() []entity.AIModelPrice {
	prices := append([]entity.AIModelPrice(nil), legacyDefaultAIModelPrices...)
	for index := range prices {
		prices[index].ContextWindowTokens = defaultAIModelContextWindows[prices[index].ModelName]
	}
	return append(prices, additionalDefaultAIModelPrices...)
}

// OpenAI model specifications, verified 2026-10-28:
// https://developers.openai.com/api/docs/models
// Unlisted gateway aliases and specialist models intentionally stay unknown.
var defaultAIModelContextWindows = map[string]int64{
	"gpt-6-astra": 1050000, "gpt-6.1-sol": 1050000,
	"gpt-6-sol": 1050000, "gpt-6-luna": 1050000,
	"gpt-5.6-sol": 1050000, "gpt-5.6-terra": 1050000, "gpt-5.6-luna": 1050000,
	"gpt-5.5": 1050000, "gpt-5.5-pro": 1050000,
	"gpt-5.4": 1050000, "gpt-5.4-pro": 1050000,
	"gpt-5.4-mini": 400000, "gpt-5.4-nano": 400000,
	"gpt-5.2": 400000, "gpt-5.2-pro": 400000,
	"gpt-5.1": 400000, "gpt-5": 400000,
	"gpt-5-mini": 400000, "gpt-5-nano": 400000, "gpt-5-pro": 400000,
	"gpt-4.1": 1047576, "gpt-4.1-mini": 1047576, "gpt-4.1-nano": 1047576,
	"gpt-4o": 128000, "gpt-4o-mini": 128000,
	"gpt-4-turbo-2024-04-09": 128000, "gpt-4-0613": 8192,
	"gpt-3.5-turbo": 16385, "gpt-3.5-turbo-0125": 16385, "gpt-3.5-turbo-1106": 16385,
	"gpt-3.5-turbo-instruct": 4096,
	"o1":                     200000, "o1-pro": 200000, "o3": 200000, "o3-pro": 200000,
	"o3-mini": 200000, "o4-mini": 200000,
	"gpt-5.3-codex": 400000, "gpt-5.6-cyber": 400000, "chat-latest": 400000,
}

// Prices remain fixed reference values in the repository's existing unit
// convention. USD numeric quotes are not exchange-rate converted. Existing
// database rows are never repriced by this catalog.
var additionalDefaultAIModelPrices = []entity.AIModelPrice{
	// https://api-docs.deepseek.com/zh-cn/quick_start/pricing/
	{ModelName: "deepseek-flash", DisplayName: "DeepSeek Flash", ContextWindowTokens: 1000000, InputPrice: 2, CachedPrice: 0.04, OutputPrice: 8, Remark: "2026-10-28 官方人民币高峰参考价；闲时价格不同，可自行调整"},
	{ModelName: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", ContextWindowTokens: 1000000, InputPrice: 9, CachedPrice: 0.30, OutputPrice: 27, Remark: "2026-10-28 官方人民币高峰参考价；闲时价格不同，可自行调整"},
	{ModelName: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", ContextWindowTokens: 1000000, InputPrice: 2, CachedPrice: 0.04, OutputPrice: 8, Remark: "2026-10-28 Flash 兼容别名；人民币高峰参考价，闲时价格不同"},
	{ModelName: "deepseek-v4-flash-vision-exp", DisplayName: "DeepSeek V4 Flash Vision Exp", ContextWindowTokens: 1000000, InputPrice: 2, CachedPrice: 0.04, OutputPrice: 8, Remark: "2026-10-28 官方实验视觉别名；人民币高峰参考价，闲时价格不同"},
	// https://platform.claude.com/docs/en/about-claude/pricing
	// https://platform.claude.com/docs/en/build-with-claude/context-windows
	{ModelName: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", ContextWindowTokens: 1000000, InputPrice: 4, CachedPrice: 0.2, CacheWritePrice: 5, OutputPrice: 20, Remark: "2026-10-28 官方短上下文参考；美元数值沿既有1:1约定，未换汇；5分钟缓存写入"},
	{ModelName: "claude-sonnet-5-5", DisplayName: "Claude Sonnet 5.5", ContextWindowTokens: 1000000, InputPrice: 2, CachedPrice: 0.1, CacheWritePrice: 2.5, OutputPrice: 10, Remark: "2026-10-28 官方短上下文参考；美元数值沿既有1:1约定，未换汇；5分钟缓存写入"},
	{ModelName: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5", ContextWindowTokens: 1000000, InputPrice: 0.1, CachedPrice: 0.01, CacheWritePrice: 0.125, OutputPrice: 0.5, Remark: "2026-10-28 官方输入≤100K参考；美元数值沿既有1:1约定，未换汇；5分钟缓存写入"},
	{ModelName: "claude-opus-4-6", DisplayName: "Claude Opus 4.6", ContextWindowTokens: 1000000, InputPrice: 5, CachedPrice: 0.5, CacheWritePrice: 6.25, OutputPrice: 25, Remark: "2026-10-28 官方参考；美元数值沿既有1:1约定，未换汇；5分钟缓存写入"},
	{ModelName: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", ContextWindowTokens: 1000000, InputPrice: 3, CachedPrice: 0.3, CacheWritePrice: 3.75, OutputPrice: 15, Remark: "2026-10-28 官方参考；美元数值沿既有1:1约定，未换汇；5分钟缓存写入"},
	{ModelName: "claude-opus-4-5", DisplayName: "Claude Opus 4.5", ContextWindowTokens: 200000, InputPrice: 5, CachedPrice: 0.5, CacheWritePrice: 6.25, OutputPrice: 25, Remark: "2026-10-28 官方参考；美元数值沿既有1:1约定，未换汇；5分钟缓存写入"},
	{ModelName: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5", ContextWindowTokens: 200000, InputPrice: 3, CachedPrice: 0.3, CacheWritePrice: 3.75, OutputPrice: 15, Remark: "2026-10-28 官方参考；美元数值沿既有1:1约定，未换汇；5分钟缓存写入"},
	{ModelName: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5", ContextWindowTokens: 200000, InputPrice: 1, CachedPrice: 0.1, CacheWritePrice: 1.25, OutputPrice: 5, Remark: "2026-10-28 官方参考；美元数值沿既有1:1约定，未换汇；5分钟缓存写入"},
	// https://help.aliyun.com/zh/model-studio/qwen3-5-plus
	// https://help.aliyun.com/zh/model-studio/qwen3-5-flash
	// https://help.aliyun.com/zh/model-studio/qwen-plus
	{ModelName: "qwen3.5-plus", DisplayName: "通义千问 Qwen3.5 Plus", ContextWindowTokens: 1000000, InputPrice: 0.8, CachedPrice: 0.08, CacheWritePrice: 1, OutputPrice: 4.8, Remark: "2026-10-28 北京≤128K人民币参考；显式缓存；非思考输出；长输入分档"},
	{ModelName: "qwen3.5-flash", DisplayName: "通义千问 Qwen3.5 Flash", ContextWindowTokens: 1000000, InputPrice: 0.2, CachedPrice: 0.02, CacheWritePrice: 0.25, OutputPrice: 2, Remark: "2026-10-28 北京≤128K人民币参考；显式缓存；长输入分档"},
	{ModelName: "qwen-plus", DisplayName: "通义千问 Qwen Plus", ContextWindowTokens: 1000000, InputPrice: 0.8, CachedPrice: 0.08, CacheWritePrice: 1, OutputPrice: 2, Remark: "2026-10-28 北京≤128K人民币参考；显式缓存；非思考输出；长输入分档"},
	// https://docs.bigmodel.cn/cn/guide/start/pricing
	// https://docs.bigmodel.cn/cn/guide/models/text/glm-5
	{ModelName: "glm-5", DisplayName: "智谱 GLM-5", ContextWindowTokens: 200000, InputPrice: 4, CachedPrice: 1, OutputPrice: 18, Remark: "2026-10-28 人民币输入<32K参考；≥32K输入/缓存/输出为6/1.5/22；缓存写入未单列"},
	// https://platform.kimi.com/docs/pricing/chat
	{ModelName: "kimi-k3.1-pro", DisplayName: "Kimi K3.1 Pro", ContextWindowTokens: 1048576, InputPrice: 13, CachedPrice: 1.3, CacheWritePrice: 13, OutputPrice: 68, Remark: "2026-10-28 官方人民币标准参考价；固定参考，可自行调整"},
	{ModelName: "kimi-k3", DisplayName: "Kimi K3", ContextWindowTokens: 1048576, InputPrice: 20, CachedPrice: 2, CacheWritePrice: 20, OutputPrice: 100, Remark: "2026-10-28 官方人民币标准参考价；固定参考，可自行调整"},
	// https://platform.minimax.cn/docs/pricing/overview
	// https://platform.minimax.io/docs/api-reference/text-anthropic-api
	{ModelName: "MiniMax-M3.1-Flash", DisplayName: "MiniMax M3.1 Flash", ContextWindowTokens: 1000000, InputPrice: 1, CachedPrice: 0.1, OutputPrice: 4, Remark: "2026-10-28 国内标准≤512K人民币参考；未单列缓存写入费用"},
	{ModelName: "MiniMax-M3", DisplayName: "MiniMax M3", ContextWindowTokens: 1000000, InputPrice: 2.1, CachedPrice: 0.42, OutputPrice: 8.4, Remark: "2026-10-28 国内标准≤512K人民币参考；未单列缓存写入费用"},
	// https://ai.google.dev/gemini-api/docs/pricing
	{ModelName: "gemini-4-argon", DisplayName: "Gemini 4 Argon", ContextWindowTokens: 1048576, InputPrice: 2, CachedPrice: 0.1, OutputPrice: 10, Remark: "2026-10-28 美元数值沿既有1:1约定，未换汇；11-30前优惠，之后输入/缓存/输出4/0.2/20；输入/输出统一窗口"},
	{ModelName: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", ContextWindowTokens: 1048576, InputPrice: 0.75, CachedPrice: 0.075, OutputPrice: 3.75, Remark: "2026-10-28 美元数值沿既有1:1约定，未换汇；12-31前优惠，之后输入/缓存/输出1.5/0.15/7.5；上下文为输入窗口"},
	{ModelName: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", ContextWindowTokens: 1048576, InputPrice: 1.25, CachedPrice: 0.125, OutputPrice: 10, Remark: "2026-10-28 美元数值沿既有1:1约定；≤200K参考，长输入2.5/0.25/15；输入窗口；小时存储费未映射缓存写入"},
	{ModelName: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", ContextWindowTokens: 1048576, InputPrice: 0.3, CachedPrice: 0.03, OutputPrice: 2.5, Remark: "2026-10-28 美元数值沿既有1:1约定，未换汇；输入窗口；小时存储费未映射缓存写入"},
	{ModelName: "gemini-2.5-flash-lite", DisplayName: "Gemini 2.5 Flash Lite", ContextWindowTokens: 1048576, InputPrice: 0.1, CachedPrice: 0.01, OutputPrice: 0.4, Remark: "2026-10-28 美元数值沿既有1:1约定，未换汇；输入窗口；小时存储费未映射缓存写入"},
}
