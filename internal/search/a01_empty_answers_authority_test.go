package search

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// The documented answers key is authoritative even when it decodes to no answer.
// Missing-key aliases and malformed-JSON free-text fallback remain compatible.
func TestA01EmptyAnswersAuthority(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"empty_array_blocks_aliases", `{"answers":[],"answer":"6","result":"7","答案":"8"}`, []string{}},
		{"null_blocks_alias", `{"answers":null,"answer":"6"}`, []string{}},
		{"invalid_value_type_blocks_alias", `{"answers":false,"answer":"6"}`, []string{}},
		{"nonempty_array_keeps_priority_and_repetition", `{"answers":["春天","春天"],"answer":"6"}`, []string{"春天", "春天"}},
		{"nonempty_string_keeps_priority", `{"answers":"春天","answer":"6"}`, []string{"春天"}},
		{"missing_answers_keeps_legacy_alias", `{"answer":"6","result":"7"}`, []string{"6"}},
		{"missing_answers_keeps_alias_fallthrough", `{"answer":"","result":"7"}`, []string{"7"}},
		{"malformed_JSON_keeps_free_text_fallback", `{"answers":["6"`, []string{`{"answers":["6"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAISearchAnswers(tc.content, nil, 15)
			observation, err := json.Marshal(map[string]any{"case": tc.name, "input": tc.content, "answers": got, "expected": tc.want})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("A01_ANSWERS_AUTHORITY_OBSERVATION %s", observation)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("answers authority: got=%v want=%v", got, tc.want)
			}
		})
	}
}

// An executed mock Responses call must still retain all usage on NO_ANSWER;
// changing the parsing result must not discard the billing source components.
func TestA01EmptyAnswersAuthorityRetainsStreamUsage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected owned mock request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected owned mock request", http.StatusBadRequest)
			return
		}
		writeOpenAIStyleStream(w, `{"answers":[],"answer":"6"}`)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "synthetic-A01-key", Model: "synthetic-a01-authority", MaxAISearchModelCalls: 1, MaxAISearchRetries: 1}
	answers, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "A01 synthetic question without attachments", Type: 15})
	gotComponents := [5]int64{usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens, usage.OutputTokens, usage.TotalTokens}
	wantComponents := [5]int64{1000, 640, 0, 20, 1020}
	targetNoAnswer := errors.Is(err, pkgerrors.ErrTargetNoAnswer)
	modelUsagePreserved := len(usage.ModelCalls) == 1
	if modelUsagePreserved {
		call := usage.ModelCalls[0]
		modelUsagePreserved = call.Index == 1 && call.Model == "synthetic-a01-authority" && [5]int64{call.InputTokens, call.CachedInputTokens, call.CacheWriteTokens, call.OutputTokens, call.TotalTokens} == wantComponents
	}
	observation, marshalErr := json.Marshal(map[string]any{"answers": answers, "target_no_answer": targetNoAnswer, "mock_HTTP_calls": calls.Load(), "usage_components": gotComponents, "model_call_usage_preserved": modelUsagePreserved, "real_provider_called": false, "DB_called": false})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	t.Logf("A01_ANSWERS_AUTHORITY_STREAM_OBSERVATION %s", observation)
	if len(answers) != 0 || !targetNoAnswer || calls.Load() != 1 || gotComponents != wantComponents || !modelUsagePreserved {
		t.Errorf("explicit empty answer must remain NO_ANSWER with one executed call and usage retained: answers=%v err=%v calls=%d usageComponents=%v modelCalls=%+v", answers, err, calls.Load(), gotComponents, usage.ModelCalls)
	}
}
