package search

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestChatGptClientUsesLongTimeoutForAISearch(t *testing.T) {
	client := (&ChatGptClient{}).getHTTPClient()

	if client.GetClient().Timeout < 180*time.Second {
		t.Fatalf("expected AI search timeout to allow slow model responses, got %s", client.GetClient().Timeout)
	}
}

type openAIRequestMessagePayload struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type openAIImagePartPayload struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

func decodeUserContentParts(t *testing.T, messages []openAIRequestMessagePayload) []openAIImagePartPayload {
	t.Helper()
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[1].Role != "user" {
		t.Fatalf("expected second message role user, got %s", messages[1].Role)
	}
	var parts []openAIImagePartPayload
	if err := json.Unmarshal(messages[1].Content, &parts); err != nil {
		t.Fatalf("decode user content parts: %v", err)
	}
	return parts
}

func TestChatGptClientSearchAnswerUsesSystemPromptAndFixedSamplingParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model       string                        `json:"model"`
			Stream      bool                          `json:"stream"`
			Temperature float64                       `json:"temperature"`
			TopP        float64                       `json:"top_p"`
			Messages    []openAIRequestMessagePayload `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Temperature != 0 {
			t.Fatalf("expected temperature=0, got %v", payload.Temperature)
		}
		if payload.TopP != 0 {
			t.Fatalf("expected top_p=0, got %v", payload.TopP)
		}
		if len(payload.Messages) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(payload.Messages))
		}
		if payload.Messages[0].Role != "system" {
			t.Fatalf("expected first message role system, got %s", payload.Messages[0].Role)
		}
		var systemPrompt string
		if err := json.Unmarshal(payload.Messages[0].Content, &systemPrompt); err != nil {
			t.Fatalf("decode system prompt: %v", err)
		}
		if systemPrompt != "系统规则 Prompt" {
			t.Fatalf("unexpected system prompt: %q", systemPrompt)
		}
		parts := decodeUserContentParts(t, payload.Messages)
		if len(parts) != 1 {
			t.Fatalf("expected one user content part, got %d", len(parts))
		}
		if parts[0].Type != "text" {
			t.Fatalf("expected text content part, got %s", parts[0].Type)
		}
		if !strings.Contains(parts[0].Text, "题目：2+2=?") {
			t.Fatalf("expected user prompt to include question, got %q", parts[0].Text)
		}
		if !strings.Contains(parts[0].Text, "A. 3") || !strings.Contains(parts[0].Text, "B. 4") {
			t.Fatalf("expected user prompt to include options, got %q", parts[0].Text)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"B\\\"]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:      true,
		APIKey:      "sk-test",
		BaseURL:     server.URL + "/v1",
		Model:       "gpt-4o-mini",
		Prompt:      "系统规则 Prompt",
		Temperature: 0.8,
		TopP:        0.9,
	}

	answer, err := client.SearchAnswer(model.SearchRequest{Question: "2+2=?", Options: []string{"3", "4"}})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}

	if !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("unexpected answer: %v", answer)
	}
}

func TestChatGptClientSearchAnswerAttachesDownloadedImagesToUserMessage(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-image-bytes"))
	}))
	defer imageServer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []openAIRequestMessagePayload `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		parts := decodeUserContentParts(t, payload.Messages)
		if len(parts) != 2 {
			t.Fatalf("expected text plus one image part, got %d", len(parts))
		}
		if parts[0].Type != "text" {
			t.Fatalf("expected first part to be text, got %s", parts[0].Type)
		}
		if !strings.Contains(parts[0].Text, "题目：识别图片") {
			t.Fatalf("expected text part to include question, got %q", parts[0].Text)
		}
		if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
			t.Fatalf("expected second part to be image_url, got %+v", parts[1])
		}
		expectedURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-image-bytes"))
		if parts[1].ImageURL.URL != expectedURL {
			t.Fatalf("expected downloaded image data url %q, got %q", expectedURL, parts[1].ImageURL.URL)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"A\\\"]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:  true,
		APIKey:  "sk-test",
		BaseURL: server.URL + "/v1",
		Model:   "gpt-4o-mini",
	}

	answer, err := client.SearchAnswer(model.SearchRequest{
		Question: "识别图片 " + imageServer.URL + "/question.png",
		Options:  []string{"A", "B"},
	})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}
	if !reflect.DeepEqual(answer, [][]string{{"A"}}) {
		t.Fatalf("unexpected answer: %v", answer)
	}
}

func TestChatGptClientSearchAnswerContinuesWhenSomeImagesFailToDownload(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("ok-image"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer imageServer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []openAIRequestMessagePayload `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		parts := decodeUserContentParts(t, payload.Messages)
		if len(parts) != 2 {
			t.Fatalf("expected one text part plus only the successfully downloaded image, got %d", len(parts))
		}
		if parts[1].ImageURL == nil {
			t.Fatalf("expected successful image part, got %+v", parts[1])
		}
		unexpectedFailedURL := "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("404 page not found\n"))
		if parts[1].ImageURL.URL == unexpectedFailedURL {
			t.Fatalf("expected failed image to be skipped, got %q", parts[1].ImageURL.URL)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"B\\\"]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:  true,
		APIKey:  "sk-test",
		BaseURL: server.URL + "/v1",
		Model:   "gpt-4o-mini",
	}

	answer, err := client.SearchAnswer(model.SearchRequest{
		Question: strings.Join([]string{
			"题目含成功图片", imageServer.URL + "/ok.png",
			"以及失败图片", imageServer.URL + "/missing.png",
		}, " "),
		Options: []string{"A", "B"},
	})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}
	if !reflect.DeepEqual(answer, [][]string{{"B"}}) {
		t.Fatalf("unexpected answer: %v", answer)
	}
}

func TestChatGptClientSearchAnswerAttachesHTMLURLAsNamedFileContent(t *testing.T) {
	htmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("<!doctype html><html><body><p>resolved formula</p></body></html>"))
	}))
	defer htmlServer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []openAIRequestMessagePayload `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		parts := decodeUserContentParts(t, payload.Messages)
		if len(parts) != 2 {
			t.Fatalf("expected text prompt plus html attachment, got %d parts", len(parts))
		}
		if parts[1].Type != "text" {
			t.Fatalf("expected html attachment to be sent as text content, got %s", parts[1].Type)
		}
		for _, expected := range []string{
			`<attached_file filename="5462425.html" content_type="text/html">`,
			"<!doctype html><html><body><p>resolved formula</p></body></html>",
			"</attached_file>",
		} {
			if !strings.Contains(parts[1].Text, expected) {
				t.Fatalf("expected html attachment to contain %q, got %q", expected, parts[1].Text)
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"B\\\"]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:  true,
		APIKey:  "sk-test",
		BaseURL: server.URL + "/v1",
		Model:   "gpt-4o-mini",
	}

	answer, err := client.SearchAnswer(model.SearchRequest{
		Question: "逻辑函数 " + htmlServer.URL + "/ananas/latex/p/5462425,应____",
	})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}
	if !reflect.DeepEqual(answer, [][]string{{"B"}}) {
		t.Fatalf("unexpected answer: %v", answer)
	}
}

func TestChatGptClientSearchAnswerUsesImageContentTypeAfterOctetStreamHeader(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/octet-stream")
		w.Header().Add("Content-Type", "image/jpeg;charset=UTF-8")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'})
	}))
	defer imageServer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []openAIRequestMessagePayload `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		parts := decodeUserContentParts(t, payload.Messages)
		if len(parts) != 2 || parts[1].Type != "image_url" || parts[1].ImageURL == nil {
			t.Fatalf("expected image attachment, got %+v", parts)
		}
		if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/jpeg;base64,") {
			t.Fatalf("expected jpeg data url, got %q", parts[1].ImageURL.URL)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"A\\\"]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:  true,
		APIKey:  "sk-test",
		BaseURL: server.URL + "/v1",
		Model:   "gpt-4o-mini",
	}

	answer, err := client.SearchAnswer(model.SearchRequest{
		Question: "识别图片 " + imageServer.URL + "/formula.jpg",
	})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}
	if !reflect.DeepEqual(answer, [][]string{{"A"}}) {
		t.Fatalf("unexpected answer: %v", answer)
	}
}

func TestChatGptClientSearchAnswerAggregatesStreamContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("unexpected authorization header: %s", got)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["model"] != "gpt-4o-mini" {
			t.Fatalf("unexpected model payload: %v", payload["model"])
		}
		if payload["stream"] != true {
			t.Fatalf("expected stream=true, got %v", payload["stream"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"第一段\\\"\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\",\\\"第二段\\\"]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:  true,
		APIKey:  "sk-test",
		BaseURL: server.URL + "/v1",
		Model:   "gpt-4o-mini",
	}

	answer, err := client.SearchAnswer(model.SearchRequest{Question: "题目", Options: []string{"A", "B"}})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}

	expected := [][]string{{"第一段", "第二段"}}
	if !reflect.DeepEqual(answer, expected) {
		t.Fatalf("expected answer %v, got %v", expected, answer)
	}
}

func TestChatGptClientSearchAnswerWithUsageCapturesStreamUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"B\\\"]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:  true,
		APIKey:  "sk-test",
		BaseURL: server.URL + "/v1",
		Model:   "gpt-4o-mini",
	}

	answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", Options: []string{"3", "4"}})
	if err != nil {
		t.Fatalf("search answer with usage: %v", err)
	}

	expectedAnswer := [][]string{{"4"}}
	if !reflect.DeepEqual(answer, expectedAnswer) {
		t.Fatalf("expected answer %v, got %v", expectedAnswer, answer)
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 || usage.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestChatGptClientSearchAnswerDebugReturnsRequestAndRawResponse(t *testing.T) {
	var capturedRequestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		capturedRequestBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"hello\\\"]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:  true,
		APIKey:  "sk-test",
		BaseURL: server.URL + "/v1",
		Model:   "gpt-4o-mini",
		Prompt:  "调试 Prompt",
	}

	answer, debug, err := client.SearchAnswerDebug(model.SearchRequest{Question: "hello"})
	if err != nil {
		t.Fatalf("search answer debug: %v", err)
	}

	if !reflect.DeepEqual(answer, [][]string{{"hello"}}) {
		t.Fatalf("unexpected debug answer: %v", answer)
	}
	if !strings.Contains(debug.RequestBody, "gpt-4o-mini") || !strings.Contains(debug.RequestBody, "hello") {
		t.Fatalf("expected request body to include model and hello, got %s", debug.RequestBody)
	}
	if debug.RequestBody != capturedRequestBody {
		t.Fatalf("expected debug request body to match sent payload, got %s vs %s", debug.RequestBody, capturedRequestBody)
	}
	if !strings.Contains(debug.RawResponse, "prompt_tokens") || !strings.Contains(debug.RawResponse, "[DONE]") {
		t.Fatalf("expected raw response to keep stream body, got %s", debug.RawResponse)
	}
	if debug.Usage.TotalTokens != 4 {
		t.Fatalf("expected usage total tokens 4, got %+v", debug.Usage)
	}
}

func TestBuildAISearchPromptIncludesQuestionTypeContext(t *testing.T) {
	prompt := buildAISearchPrompt(model.SearchRequest{
		Question: "请指出下列案例属于何种类比创新方法?",
		Type:     2,
	})

	if !strings.Contains(prompt, "题型：填空题/连线题") {
		t.Fatalf("expected prompt to include question type context, got %q", prompt)
	}
	if !strings.Contains(prompt, "题目：请指出下列案例属于何种类比创新方法?") {
		t.Fatalf("expected prompt to include question text, got %q", prompt)
	}
}

func TestBuildAISearchPromptNormalizesChaoxingStyleOptionPrefixes(t *testing.T) {
	prompt := buildAISearchPrompt(model.SearchRequest{
		Question:   "  请完成连线。 ",
		Type:       2,
		Options:    []string{"A. 左侧术语", "B. 右侧案例"},
		CourseName: "创新方法学",
		Extra:      "来自 chaoxing_plus OCR",
	})

	checks := []string{
		"题型：填空题/连线题",
		"题型补充说明：该题型在调用侧同时承载填空题、连线题、匹配题；如果题目要求建立对应关系，请按每组对应关系依次返回 answers 数组。",
		"题目：请完成连线。",
		"课程：创新方法学",
		"补充信息：来自 chaoxing_plus OCR",
		"A. 左侧术语",
		"B. 右侧案例",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("expected prompt %q to contain %q", prompt, check)
		}
	}
	unexpected := []string{
		"A. A. 左侧术语",
		"B. B. 右侧案例",
	}
	for _, check := range unexpected {
		if strings.Contains(prompt, check) {
			t.Fatalf("expected prompt %q not to contain duplicated option prefix %q", prompt, check)
		}
	}
}
