package util

import (
	"math"
	"strings"
)

// 本文件实现「多来源答案的加权聚合」，是 SearchRightAnswer 的可选增强层。
//
// 背景与依据（上游 chaoxing-exam/README.md）：
//
//	「单一模型直接看图答题，实测 50 题错 3 题。拆成『看』和『想』两步，
//	 各取所长，准确率显著提升。」
//	「首次答题正确率约 65-75%…」
//
// 上游的结论是「单次结果不可靠，多次/多来源才可靠」。本项目 /search 链路
// 本来就是多 provider 并发聚合（internal/controller/search.go 把各渠道结果
// append 成 [][]string），但聚合方式只是「取出现次数最多的那一组，并列取最先出现的」——
// **并列时它仍然会吐出一个答案**，这等于在证据不足时硬猜。
//
// 因此这里提供一层可选的加权聚合：
//   - 权重可以由「出现位置」近似（provider 结果按完成顺序 append，
//     先完成的通常也是先命中的，但这一近似并不严格，故默认不启用衰减）；
//   - 并列时可以选择**弃权**（返回空 + 低置信度标记），而不是硬猜第一个。
//
// **默认零值行为与 SearchRightAnswer 完全一致**：不作任何线上行为改变。
// 这是刻意的 —— 绝大多数题库只返回一个来源，若把「并列即弃权」设成默认，
// 这些题会从「有答案」退化成「无答案」，属于用准确率换召回率的净损失。

// AnswerAggregationOptions 控制加权聚合的取舍。
//
// 零值等价于 SearchRightAnswer 的既有行为（计票 + 并列取最先出现）。
type AnswerAggregationOptions struct {
	// PositionDecay 是按答案组出现顺序施加的权重衰减底数：
	// 第 i 组（0 基）权重为 PositionDecay^i。
	//
	// <= 1 表示**不衰减**（所有权重为 1，退化为计票）。
	// 之所以默认不衰减：internal/controller/search.go 里各 provider 是并发
	// goroutine append 的，顺序由完成时刻决定而非可靠度，把顺序当可靠度
	// 只能是近似，不能默认开启。
	PositionDecay float64

	// AbstainOnTie 为 true 时，加权得分并列且没有其它证据可区分，返回空答案
	// 并置 LowConfidence。为 false 时沿用「取最先出现的那一组」。
	AbstainOnTie bool

	// MinSupport 要求胜出组至少被 MinSupport 个来源给出，否则视为证据不足。
	// 0 或 1 表示不设门槛。
	MinSupport int
}

// AnswerAggregation 是加权聚合的完整结果。
//
// 单独暴露结构化结果，是因为「返回空」本身无法区分「没有答案」与
// 「有多个同样可能的答案，所以不敢选」——调用方需要能区分这两种情况。
type AnswerAggregation struct {
	Answer []string

	// Confidence 是胜出组的加权得分占全部权重的比例，取值 [0,1]。
	// 没有任何输入时为 0。
	Confidence float64

	// LowConfidence 表示「存在多个并列最高分，结论不可信」。
	// 注意：它只在 AbstainOnTie=true 且确实并列时才为 true；
	// 单来源输入永远是高置信度的（单一证据谈不上冲突）。
	LowConfidence bool

	// Support 是胜出组被多少个来源组给出。
	Support int
	// Total 是参与聚合的答案组总数。
	Total int
}

// AnswerAggregationEpsilon 用于浮点权重比较。
//
// 权重是 PositionDecay^i 的累加，直接 == 比较会因精度抖动
// 把本该并列的判成不并列（或反之）。
const AnswerAggregationEpsilon = 1e-9

// AggregateAnswers 按选项做加权聚合，返回结构化结果。
//
// 判定顺序：
//  1. 按「组键」（组内各元素用 sep 连接）汇总权重与出现次数；
//  2. 取权重总和最高的组；
//  3. 若最高分有多个组并列：
//     - opts.AbstainOnTie=true  → 返回空答案 + LowConfidence=true；
//     - opts.AbstainOnTie=false → 取最先出现的那一组（与 SearchRightAnswer 一致）；
//  4. 若胜出组的出现次数 < opts.MinSupport → 返回空答案（不算并列，属证据不足）。
//
// 返回的答案是**副本**，调用方改它不会污染入参。
func AggregateAnswers(answers [][]string, opts AnswerAggregationOptions) AnswerAggregation {
	result := AnswerAggregation{Total: len(answers)}
	if len(answers) == 0 {
		return result
	}

	type groupStat struct {
		answer  []string
		weight  float64
		support int
		first   int
	}

	stats := make(map[string]*groupStat, len(answers))
	order := make([]string, 0, len(answers))
	totalWeight := 0.0

	for i, group := range answers {
		key := strings.Join(group, sep)
		weight := 1.0
		if opts.PositionDecay > 1 {
			weight = math.Pow(opts.PositionDecay, float64(i))
		}
		totalWeight += weight

		stat, exists := stats[key]
		if !exists {
			stats[key] = &groupStat{
				answer:  append([]string(nil), group...),
				weight:  weight,
				support: 1,
				first:   i,
			}
			order = append(order, key)
			continue
		}
		stat.weight += weight
		stat.support++
	}

	bestWeight := -1.0
	for _, key := range order {
		if stats[key].weight > bestWeight {
			bestWeight = stats[key].weight
		}
	}

	var winners []string
	for _, key := range order {
		if math.Abs(stats[key].weight-bestWeight) <= AnswerAggregationEpsilon {
			winners = append(winners, key)
		}
	}

	if totalWeight > 0 {
		result.Confidence = bestWeight / totalWeight
	}

	// 支持数门槛在**并列判定之前**生效：门槛是对「有没有足够证据」的判断，
	// 与「证据之间是否互相一致」是两个独立问题。
	// 顺序搞反会让「三个来源各说一套、每组都只有 1 票」在 MinSupport=2 下
	// 仍然凭并列逻辑挑出第一个 —— 那正是门槛要挡的情况。
	if opts.MinSupport > 1 && stats[winners[0]].support < opts.MinSupport {
		result.LowConfidence = len(winners) > 1
		return result
	}

	// 并列：两个以上组同分。
	if len(winners) > 1 {
		result.LowConfidence = true
		if opts.AbstainOnTie {
			// 刻意返回空答案：证据不支持任何单一结论时，宁可弃权。
			// 依据见本文件顶部——上游靠「提交后拿标准答案重做」兜底，
			// 我们这边没有那条回路，硬猜一个错的代价更高。
			return result
		}
		// 不弃权：沿用「最先出现」的确定性顺序。
		best := stats[winners[0]]
		result.Answer = append([]string(nil), best.answer...)
		result.Support = best.support
		return result
	}

	best := stats[winners[0]]
	result.Answer = append([]string(nil), best.answer...)
	result.Support = best.support
	return result
}

// SearchRightAnswerWeighted 是 SearchRightAnswer 的可选加权版本。
//
// 传入零值 AnswerAggregationOptions 时，结果与 SearchRightAnswer **逐字节等价**
// （含并列取最先出现的语义），因此可以安全地在需要时替换。
// 需要「并列即弃权」时显式传 AbstainOnTie: true。
func SearchRightAnswerWeighted(answers [][]string, opts AnswerAggregationOptions) []string {
	return AggregateAnswers(answers, opts).Answer
}
