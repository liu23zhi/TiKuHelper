package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// TestA01OfflineQualitySampleMatrixService preserves six observed local helper
// contracts. The false call labels below describe this fixture's static scope,
// not system-wide provider/DB instrumentation or a model-quality evaluation.
func TestA01OfflineQualitySampleMatrixService(t *testing.T) {
	records := []map[string]any{}
	ids := []string{"A01_F01_READING_REPEAT_NOISY_OPTIONS", "A01_F02_MATERIAL_COPY_ACCEPTED", "A01_F03_UNCERTAIN_GROUP_NOANSWER", "A01_F04_UNSAFE_UNCERTAIN_PRIORITY", "A01_F05_PUBLIC_SUCCESS_JSON", "A01_F06_PUBLIC_FAILURE_JSON"}
	for _, id := range ids {
		id := id
		t.Run(id, func(tt *testing.T) {
			r := map[string]any{"case_id": id, "DB_called": false, "real_provider_called": false, "real_model_called": false}
			guard := func(ok bool, msg string) {
				if !ok {
					tt.Error("A01_CONTRACT_GUARD " + msg)
				}
			}
			switch id {
			case "A01_F01_READING_REPEAT_NOISY_OPTIONS":
				req := model.SearchRequest{Question: "合成阅读题，两问同为春天", Type: 15, Options: []string{"段落格式字体字号", `var a01noise = "";`}}
				got := normalizeAIFallbackAnswersForRequest([]string{"春天", "春天"}, req)
				r["input"] = req
				r["answers"] = got
				guard(reflect.DeepEqual(got, []string{"春天", "春天"}), "all nonobjective duplicates preserved")
			case "A01_F02_MATERIAL_COPY_ACCEPTED":
				in := [][]string{{"材料：我每周种2株树。"}}
				got, code := collectUsableAIFallbackAnswers(in)
				r["input_answer_groups"] = in
				r["usable_answers"] = got
				r["error_code"] = code
				r["semantic_answer_is_synthetic_gold6"] = reflect.DeepEqual(got, []string{"6"})
				guard(len(got) == 1 && code == "", "record current free text acceptance, do not add material-rejection policy")
			case "A01_F03_UNCERTAIN_GROUP_NOANSWER":
				in := [][]string{{" ", "\"不确定\""}, {"不清楚！"}}
				got, code := collectUsableAIFallbackAnswers(in)
				r["input_answer_groups"] = in
				r["usable_answers"] = got
				r["error_code"] = code
				guard(len(got) == 0 && code == AIFallbackErrorCodeNoAnswer, "current punctuation+multiple-group no answer classification")
			case "A01_F04_UNSAFE_UNCERTAIN_PRIORITY":
				in := [][]string{{"无法回答"}, {"不确定"}}
				got, code := collectUsableAIFallbackAnswers(in)
				r["input_answer_groups"] = in
				r["usable_answers"] = got
				r["error_code"] = code
				guard(len(got) == 0 && code == AIFallbackErrorCodeUnsafeToAnswer, "preserve existing mixed priority, not new refusal policy")
			case "A01_F05_PUBLIC_SUCCESS_JSON", "A01_F06_PUBLIC_FAILURE_JSON":
				result := AIFallbackResult{Success: true, Result: &AIFallbackAnswer{Question: "合成问题3乘2", Answer: "6"}}
				if id == "A01_F06_PUBLIC_FAILURE_JSON" {
					result = buildAIFallbackErrorResult(AIFallbackErrorCodeNoAnswer, "未找到可用答案")
				}
				result.Usage = internalsearch.AISearchUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
				result.Model = "A01_PRIVATE_MODEL"
				result.FailureDetail = "A01_PRIVATE_DIAGNOSTIC"
				result.UnpricedModel = true
				raw, e := json.Marshal(result)
				var payload map[string]any
				if e == nil {
					e = json.Unmarshal(raw, &payload)
				}
				r["public_JSON"] = string(raw)
				r["DB_call"] = false
				guard(e == nil && len(payload) == 2 && !strings.Contains(string(raw), "PRIVATE") && !strings.Contains(string(raw), "Usage") && !strings.Contains(string(raw), "Unpriced"), "existing public shape hides private/cost metadata")
				if id == "A01_F05_PUBLIC_SUCCESS_JSON" {
					guard(payload["success"] == true && payload["result"] != nil, "success shape")
				} else {
					guard(payload["success"] == false && payload["error"] != nil, "failure shape")
				}
			}
			r["contract_guard_passed"] = !tt.Failed()
			raw, e := json.Marshal(r)
			if e != nil {
				tt.Fatal(e)
			}
			tt.Logf("A01_OFFLINE_SERVICE_CASE_JSON %s", raw)
			records = append(records, r)
		})
	}
	raw, e := json.Marshal(map[string]any{"schema": "A01_OFFLINE_SERVICE_LEDGER_V1", "cases": records, "sample_count": len(records), "model_accuracy_or_Judge_not_evaluated": true, "product_policy_changed": false})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("A01_OFFLINE_SERVICE_LEDGER_JSON %s", raw)
}
