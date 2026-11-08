package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// Ordinary CI copy of the accepted A01 offline matrix: no private marker, socket,
// provider, model or DB is required. S03/S04 use the already-admitted delivered-
// image-only numbering; HTML/failed attachments never consume an image ordinal.
const a01OfflineMatrixPNG64 = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAAD0lEQVR4nGP4DwQMDAz/ARruBPywhCTXAAAAAElFTkSuQmCC"

type a01OfflineMatrixNoNetworkTransport struct {
	mu    sync.Mutex
	calls int
}

func (tr *a01OfflineMatrixNoNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tr.mu.Lock()
	tr.calls++
	tr.mu.Unlock()
	return nil, errors.New("A01_INFRA unexpected default transport call; no network delegated")
}

func a01OfflineMatrixDenyDefaultNetwork(t *testing.T) {
	t.Helper()
	previous := http.DefaultTransport
	closed := &a01OfflineMatrixNoNetworkTransport{}
	http.DefaultTransport = closed
	t.Cleanup(func() {
		current := http.DefaultTransport
		http.DefaultTransport = previous
		// closed is a comparable pointer, not a function-valued interface.
		if current != closed {
			t.Error("A01_INFRA default transport changed during sequential matrix")
		}
		closed.mu.Lock()
		calls := closed.calls
		closed.mu.Unlock()
		if calls != 0 {
			t.Errorf("A01_INFRA unexpected default transport calls=%d", calls)
		}
	})
}

type a01OfflineMatrixTransport struct {
	mu         sync.Mutex
	paths      []string
	unexpected bool
}

func (tr *a01OfflineMatrixTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	tr.paths = append(tr.paths, req.URL.Path)
	tr.mu.Unlock()
	reject := func() (*http.Response, error) {
		tr.mu.Lock()
		tr.unexpected = true
		tr.mu.Unlock()
		return nil, errors.New("A01_INFRA no outbound transport")
	}
	if req.URL.Scheme != "http" || req.URL.Host != "a01-fixture.invalid" || req.Method != http.MethodGet || req.URL.RawQuery != "" {
		return reject()
	}
	status, contentType := http.StatusOK, "image/png"
	body, err := base64.StdEncoding.DecodeString(a01OfflineMatrixPNG64)
	if err != nil {
		return reject()
	}
	switch req.URL.Path {
	case "/formula.png":
	case "/material.html":
		contentType = "text/html"
		body = []byte("<p>材料：每次增加2株。</p><script>A01_EDITOR_ONLY()</script><p>问题：3次是多少？</p>")
	case "/failed.png":
		status, body = http.StatusNotFound, nil
	default:
		return reject()
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: req}, nil
}

type a01OfflineMatrixFailingTool struct {
	Calls     int
	FetchFull bool
}

func (tool *a01OfflineMatrixFailingTool) SearchAggregate(ctx context.Context, req uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	tool.Calls++
	tool.FetchFull = req.FetchFull
	return uapi.SearchAggregateResponse{}, errors.New("A01_PRIVATE_SYNTHETIC_TOOL_DIAGNOSTIC")
}

func TestA01OfflineQualitySampleMatrixSearch(t *testing.T) {
	a01OfflineMatrixDenyDefaultNetwork(t)
	records := []map[string]any{}
	ids := []string{
		"A01_S01_MATERIAL_PROMPT", "A01_S02_RICH_QUESTION_RAW",
		"A01_S03_IMAGE_HTML_ORDER", "A01_S04_FAILED_FIRST_IMAGE_REFERENCE",
		"A01_S05_JSON_EXTRA_FIELDS", "A01_S06_PLAINTEXT_LEGACY",
		"A01_S07_MALFORMED_JSON_LEGACY", "A01_S08_EMPTY_ANSWERS",
		"A01_S09_MATERIAL_COPY_ACCEPTED", "A01_S10_TOOL_FAILURE",
		"A01_S11_TOOL_UNKNOWN_ARGUMENT", "A01_S12_TOOL_MULTIPLE_JSON",
		"A01_S13_USAGE_COMPONENT_CLAMP", "A01_S14_USAGE_ACCUMULATION",
	}
	for _, id := range ids {
		id := id
		t.Run(id, func(tt *testing.T) {
			r := map[string]any{"case_id": id, "real_model_called": false, "real_provider_called": false, "DB_called": false}
			guard := func(ok bool, message string) {
				if !ok {
					tt.Error("A01_CONTRACT_GUARD " + message)
				}
			}
			switch id {
			case "A01_S01_MATERIAL_PROMPT":
				question := "材料：我每周种2株树。问题：3周共种多少株？"
				req := model.SearchRequest{Question: question, Type: 15, CourseName: "合成算术", Extra: "只比较材料与问题入口，不评测模型"}
				prompt := buildAISearchPrompt(req)
				r["input"], r["prompt"] = req, prompt
				guard(strings.Contains(prompt, question) && strings.Contains(prompt, "阅读理解"), "material and question/type remain visible")
			case "A01_S02_RICH_QUESTION_RAW":
				question := "<p>材料：每次2株。</p><script>A01_EDITOR_ONLY()</script><p>问题：3次是多少？</p>"
				req := model.SearchRequest{Question: question, Type: 4, Options: []string{"var a01noise = 1;", "段落格式字体字号点击上传x这是一段足够长的合法考生作答内容"}}
				before := append([]string(nil), req.Options...)
				prompt := buildAISearchPrompt(req)
				r["input"], r["prompt"] = req, prompt
				r["question_rich_noise_preserved"] = strings.Contains(prompt, "A01_EDITOR_ONLY")
				r["option_script_removed"] = !strings.Contains(prompt, "var a01noise")
				guard(reflect.DeepEqual(before, req.Options) && strings.Contains(prompt, question) && !strings.Contains(prompt, "var a01noise"), "pure prompt construction preserves caller and existing conservative option filter")
			case "A01_S03_IMAGE_HTML_ORDER", "A01_S04_FAILED_FIRST_IMAGE_REFERENCE":
				first, second := "http://a01-fixture.invalid/formula.png", "http://a01-fixture.invalid/material.html"
				wantImage, wantHTML := first, true
				wantPaths := []string{"/formula.png", "/material.html"}
				wantTypes := []string{"image_url", "text"}
				if id == "A01_S04_FAILED_FIRST_IMAGE_REFERENCE" {
					first, second = "http://a01-fixture.invalid/failed.png", "http://a01-fixture.invalid/formula.png"
					wantImage, wantHTML = second, false
					wantPaths = []string{"/failed.png", "/formula.png"}
					wantTypes = []string{"image_url"}
				}
				req := model.SearchRequest{Question: "材料与问题必须一起看 " + first + " " + second, Type: 15}
				transport := &a01OfflineMatrixTransport{}
				parts := buildAISearchPromptParts(req, &http.Client{Transport: transport})
				images, referenceCount, referenceIndex := 0, 0, -1
				html, references := false, ""
				attachmentTypes := []string{}
				for i, part := range parts {
					if i == 0 {
						continue
					}
					if part.Type == "text" && strings.HasPrefix(part.Text, "图片对应关系（") {
						referenceCount++
						referenceIndex, references = i, part.Text
						continue
					}
					attachmentTypes = append(attachmentTypes, part.Type)
					if part.Type == "image_url" {
						images++
						guard(part.ImageURL != nil && strings.HasPrefix(part.ImageURL.URL, "data:image/png;base64,"), "synthetic image data URI")
					}
					html = html || strings.Contains(part.Text, "<attached_file")
				}
				transport.mu.Lock()
				paths, unexpected := append([]string(nil), transport.paths...), transport.unexpected
				transport.mu.Unlock()
				sort.Strings(paths)
				// Approved current contract: count only the successful image parts.
				// Do not restore old S03/S04 undesired HTML/404 image ordinals.
				wantReferences := "图片对应关系（图片按出现顺序编号，请结合图号理解题目）：\n图一为：" + wantImage
				r["input"], r["attachment_paths"] = req, paths
				r["image_parts"], r["HTML_material_part"] = images, html
				r["reference_text"], r["expected_reference_text"] = references, wantReferences
				r["attachment_part_types"] = attachmentTypes
				r["failed_first_reference_misalignment_observation"] = id == "A01_S04_FAILED_FIRST_IMAGE_REFERENCE" && images == 1 && strings.Contains(references, "图一为："+first)
				guard(reflect.DeepEqual(paths, wantPaths) && !unexpected && images == 1, "actual supplied inprocess transport only and both declared URLs requested once")
				guard(len(parts) > 0 && parts[0].Type == "text" && parts[0].Text == buildAISearchPrompt(req), "original first prompt preserves both declared URLs")
				guard(html == wantHTML && reflect.DeepEqual(attachmentTypes, wantTypes), "HTML remains text and failed attachment does not become an image")
				guard(referenceCount == 1 && referenceIndex == len(parts)-1 && references == wantReferences, "delivered-image-only numbering and final reference part")
			case "A01_S05_JSON_EXTRA_FIELDS", "A01_S06_PLAINTEXT_LEGACY", "A01_S07_MALFORMED_JSON_LEGACY", "A01_S08_EMPTY_ANSWERS", "A01_S09_MATERIAL_COPY_ACCEPTED":
				content := `{"answers":["6"],"explanation":"extra metadata"}`
				want := []string{"6"}
				switch id {
				case "A01_S06_PLAINTEXT_LEGACY":
					content = "6"
				case "A01_S07_MALFORMED_JSON_LEGACY":
					content = `{"answers":["6"`
					want = []string{content}
				case "A01_S08_EMPTY_ANSWERS":
					content, want = `{"answers":[]}`, []string{}
				case "A01_S09_MATERIAL_COPY_ACCEPTED":
					content = `{"answers":["材料：我每周种2株树。"]}`
					want = []string{"材料：我每周种2株树。"}
				}
				got := parseAISearchAnswers(content, nil, 15)
				r["input_content"], r["answers"] = content, got
				r["JSON_syntax_valid"] = json.Valid([]byte(content))
				r["equals_synthetic_gold6"] = reflect.DeepEqual(got, []string{"6"})
				r["semantic_model_quality_evaluated"] = false
				guard(reflect.DeepEqual(got, want), "freeze current answers/fallback behavior, not new refusal policy")
			case "A01_S10_TOOL_FAILURE", "A01_S11_TOOL_UNKNOWN_ARGUMENT", "A01_S12_TOOL_MULTIPLE_JSON":
				arguments := `{"query":"合成算术6"}`
				if id == "A01_S11_TOOL_UNKNOWN_ARGUMENT" {
					arguments = `{"query":"合成算术6","unexpected":true}`
				}
				if id == "A01_S12_TOOL_MULTIPLE_JSON" {
					arguments = `{"query":"合成算术6"}{"query":"另一份"}`
				}
				tool := &a01OfflineMatrixFailingTool{}
				client := &ChatGptClient{SearchTool: tool}
				count, contentBytes := 0, 0
				body, abort := client.executeAISearchTool(context.Background(), openAIToolCall{ID: "a01-synthetic", Type: "function", Function: openAIToolFunction{Name: "web_search", Arguments: arguments}}, &count, 3, map[string]string{}, &contentBytes, &AISearchUsage{}, 1, 4)
				var payload map[string]string
				err := json.Unmarshal([]byte(body), &payload)
				r["input_arguments"], r["tool_response_JSON"] = arguments, payload
				r["abort"], r["synthetic_tool_calls"], r["search_count"] = abort, tool.Calls, count
				guard(err == nil && payload["error"] != "" && !strings.Contains(body, "PRIVATE_SYNTHETIC"), "existing bounded failure JSON, no private diagnostic echo")
				if id == "A01_S10_TOOL_FAILURE" {
					guard(abort && tool.Calls == 1 && count == 1 && !tool.FetchFull, "actual synthetic tool failure branch")
				} else {
					guard(!abort && tool.Calls == 0 && count == 0, "actual argument rejection before supplied tool")
				}
			case "A01_S13_USAGE_COMPONENT_CLAMP":
				input := `{"input_tokens":5,"output_tokens":2,"total_tokens":0,"input_tokens_details":{"cached_tokens":9}}`
				var raw openAIResponsesUsage
				err := json.Unmarshal([]byte(input), &raw)
				usage := buildAISearchUsageFromResponses(&raw)
				r["input_usage_JSON"], r["usage_components"] = input, usage
				r["cached_plus_write_may_overlap"] = usage.CachedInputTokens+usage.CacheWriteTokens > usage.InputTokens
				guard(err == nil && reflect.DeepEqual(usage, AISearchUsage{InputTokens: 5, CachedInputTokens: 5, OutputTokens: 2, TotalTokens: 7}), "Responses input/cached/total clamp; legacy cache_creation wire remains explicitly unmapped")
			case "A01_S14_USAGE_ACCUMULATION":
				first := AISearchUsage{InputTokens: 5, CachedInputTokens: 2, CacheWriteTokens: 1, OutputTokens: 2, TotalTokens: 7}
				second := AISearchUsage{InputTokens: 3, CachedInputTokens: 1, CacheWriteTokens: 2, OutputTokens: 4, TotalTokens: 7}
				addAISearchUsage(&first, second)
				r["usage_components"] = first
				guard(reflect.DeepEqual(first, AISearchUsage{InputTokens: 8, CachedInputTokens: 3, CacheWriteTokens: 3, OutputTokens: 6, TotalTokens: 14}), "preserve cost-related token components only")
			}
			r["contract_guard_passed"] = !tt.Failed()
			body, err := json.Marshal(r)
			if err != nil {
				tt.Fatal(err)
			}
			tt.Logf("A01_OFFLINE_SEARCH_CASE_JSON %s", body)
			records = append(records, r)
		})
	}
	body, err := json.Marshal(map[string]any{"schema": "A01_OFFLINE_SEARCH_LEDGER_V1", "cases": records, "sample_count": len(records), "model_accuracy_or_Judge_not_evaluated": true, "product_policy_changed": false})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("A01_OFFLINE_SEARCH_LEDGER_JSON %s", body)
}
