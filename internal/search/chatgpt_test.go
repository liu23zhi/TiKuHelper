package search

import (
	"encoding/base64"
	"encoding/json"
	"errors"
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

	if client.GetClient().Timeout != 0 {
		t.Fatalf("expected AI search client to have no timeout, got %s", client.GetClient().Timeout)
	}
	transport, ok := client.GetClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected AI search client to use http.Transport, got %T", client.GetClient().Transport)
	}
	if transport.ResponseHeaderTimeout != aiSearchFirstTokenTimeout {
		t.Fatalf("expected AI search response header timeout %s, got %s", aiSearchFirstTokenTimeout, transport.ResponseHeaderTimeout)
	}

	attachmentClient := (&ChatGptClient{}).getAttachmentHTTPClient()
	if attachmentClient.GetClient().Timeout != aiSearchAttachmentTimeout {
		t.Fatalf("expected image download client timeout %s, got %s", aiSearchAttachmentTimeout, attachmentClient.GetClient().Timeout)
	}
	attachmentTransport, ok := attachmentClient.GetClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected image download client to use http.Transport, got %T", attachmentClient.GetClient().Transport)
	}
	if attachmentTransport.ResponseHeaderTimeout != aiSearchAttachmentTimeout {
		t.Fatalf("expected image download response header timeout %s, got %s", aiSearchAttachmentTimeout, attachmentTransport.ResponseHeaderTimeout)
	}
}

func TestBuildAISearchPromptPartsAddsImageReferencesAfterAttachments(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Referer(); got != "https://mooc1.chaoxing.com/" {
			t.Fatalf("expected Chaoxing referer, got %q", got)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-image-bytes"))
	}))
	defer imageServer.Close()

	firstURL := imageServer.URL + "/equation.png"
	secondURL := imageServer.URL + "/input.png"
	parts := buildAISearchPromptParts(model.SearchRequest{
		Question: "差分方程 " + firstURL + "，其中:" + secondURL + "为输入",
	}, imageServer.Client())

	if len(parts) != 4 {
		t.Fatalf("expected text, two images, and image reference text, got %d parts", len(parts))
	}
	if parts[1].Type != "image_url" || parts[2].Type != "image_url" {
		t.Fatalf("expected image parts before reference text, got %+v", parts)
	}
	if parts[3].Type != "text" {
		t.Fatalf("expected final image reference text part, got %s", parts[3].Type)
	}
	for _, expected := range []string{
		"图一为：" + firstURL,
		"图二为：" + secondURL,
	} {
		if !strings.Contains(parts[3].Text, expected) {
			t.Fatalf("expected final reference text %q to contain %q", parts[3].Text, expected)
		}
	}
}

func TestReadOpenAIStreamContentWaitsForFirstTokenOnlyWithinDeadline(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n"))
		_ = writer.Close()
	}()

	started := time.Now()
	content, _, _, err := readOpenAIStreamContentWithFirstTokenTimeout(reader, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("expected stream to receive its first token, got %v", err)
	}
	if content != "first" {
		t.Fatalf("expected aggregated content, got %q", content)
	}
	if time.Since(started) < 50*time.Millisecond {
		t.Fatalf("expected reader to wait for first token")
	}
}

func TestReadOpenAIStreamContentReturnsFirstTokenTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	started := time.Now()
	_, _, _, err := readOpenAIStreamContentWithFirstTokenTimeout(reader, 20*time.Millisecond)
	if !errors.Is(err, errAISearchFirstTokenTimeout) {
		t.Fatalf("expected first-token timeout, got %v", err)
	}
	if time.Since(started) < 20*time.Millisecond {
		t.Fatalf("expected timeout to wait for configured deadline")
	}
}

func TestParseAISearchAnswersPreservesRepeatedFillAnswers(t *testing.T) {
	content := `{"answers":["北京","北京"]}`

	fillAnswers := parseAISearchAnswers(content, nil, 2)
	if !reflect.DeepEqual(fillAnswers, []string{"北京", "北京"}) {
		t.Fatalf("expected repeated fill answers to stay in order, got %v", fillAnswers)
	}

	choiceAnswers := parseAISearchAnswers(content, nil, 0)
	if !reflect.DeepEqual(choiceAnswers, []string{"北京"}) {
		t.Fatalf("expected objective answers to remain deduplicated, got %v", choiceAnswers)
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
		// 普通搜索不播报预算：system prompt 只保留自定义提示词 + 直出答案要求。
		if !strings.HasPrefix(systemPrompt, "系统规则 Prompt\n本轮不提供任何联网/读取工具") {
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
		if len(parts) != 3 {
			t.Fatalf("expected text, one image, and image references, got %d", len(parts))
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
		if len(parts) != 3 {
			t.Fatalf("expected text, successful image, and image references, got %d", len(parts))
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
		if len(parts) != 3 {
			t.Fatalf("expected text prompt, html attachment, and image references, got %d parts", len(parts))
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
		if len(parts) != 3 || parts[1].Type != "image_url" || parts[1].ImageURL == nil {
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
	if !strings.Contains(prompt, "如果题目包含多个小问，请按原编号完整回答每一问") {
		t.Fatalf("expected prompt to require complete multipart answers, got %q", prompt)
	}
}

func TestDefaultAISearchPromptCoversObjectiveAndTextQuestionTypes(t *testing.T) {
	prompt := DefaultAISearchPrompt()
	for _, expected := range []string{
		"判断题：只返回 1 个判断结果",
		"填空题：按题干中空的出现顺序",
		"简答题、问答题、阅读理解、大作业：必须回答全部小问",
		"保留必要的推导、公式、依据和结论",
		"图X为",
		"只有在题目、选项和图片全部缺失、完全无法判断题意时",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected default prompt %q to contain %q", prompt, expected)
		}
	}
}

func TestDefaultAISearchPromptKeepsStudentPersonaAndForbidsUncertainAnswers(t *testing.T) {
	prompt := DefaultAISearchPrompt()
	for _, expected := range []string{
		"【作答身份】",
		"第一人称作答",
		"绝对不要用“不知道”“不确定”“无法确定”",
		"遇到“你能不能……”“你是否愿意……”这类表态题或自我要求题",
		"直接给出正面、具体、可执行的承诺",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected default prompt %q to contain %q", prompt, expected)
		}
	}
}

func TestDefaultAISearchPromptIsSingleSourceSharedWithServiceLayer(t *testing.T) {
	// service 层与 search 层必须使用同一份提示词，否则配置页显示的内容会与实际生效的不一致。
	if got := DefaultAISearchPrompt(); got == "" || !strings.Contains(got, "【分题型规则】") {
		t.Fatalf("expected exported default prompt to be complete, got %q", got)
	}
}

// 回归：离散 LSI 系统差分方程题（图片以透明底 PNG 给出）曾经稳定返回「不确定」。
// 用户提示必须带上图号对应关系、小问编号要求和禁止推脱的约束。
func TestBuildAISearchPromptKeepsFormulaImageQuestionAnswerable(t *testing.T) {
	const (
		equationURL = "https://p.ananas.chaoxing.com/star3/origin/3969438ce57a85e4d3fa8d3faaa0bc87.png"
		inputURL    = "https://p.ananas.chaoxing.com/star3/origin/05baca4bfd6fad9af6de874c8e2f809e.png"
		outputURL   = "https://p.ananas.chaoxing.com/star3/origin/3ac3d0da42cf634dedc24d1dafcb70d7.png"
		systemURL   = "https://p.ananas.chaoxing.com/star3/origin/52d1f17e764fcc69058249fe46ba52f2.png"
	)

	question := "已知离散 LSI 系统的差分方程: " + equationURL +
		" 其中:" + inputURL + "为输入," + outputURL + "为输出 " +
		"1)求系统函数" + systemURL + ",指出系统的零极点; 2)若该系统是因果稳定的,指出系统的收敛域; 3)求该因果稳定系统的单位冲激响应"

	parts := buildAISearchPromptParts(model.SearchRequest{Question: question, Type: 4}, nil)
	if len(parts) != 2 {
		t.Fatalf("expected user text plus image reference text, got %d parts", len(parts))
	}

	userText := parts[0].Text
	if !strings.Contains(userText, "题型：问答题/简答题/阅读理解题") {
		t.Fatalf("expected text question type label, got %q", userText)
	}
	for _, check := range []string{
		"1)求系统函数",
		"3)求该因果稳定系统的单位冲激响应",
		"按原编号完整回答每一问",
		"不要返回“不确定”“不知道”或“无法确定”",
	} {
		if !strings.Contains(userText, check) {
			t.Fatalf("expected user prompt %q to contain %q", userText, check)
		}
	}

	// 四张公式图必须按出现顺序编号，模型才能把「图一为…图三为输出」对应到题干。
	referenceText := parts[1].Text
	if !strings.Contains(referenceText, "图片按出现顺序编号") {
		t.Fatalf("expected image numbering hint, got %q", referenceText)
	}
	for _, check := range []string{
		"图一为：" + equationURL,
		"图二为：" + inputURL,
		"图三为：" + outputURL,
		"图四为：" + systemURL,
	} {
		if !strings.Contains(referenceText, check) {
			t.Fatalf("expected image reference %q to contain %q", referenceText, check)
		}
	}
}

// 回归：大作业/阅读理解/填空/判断等题型都要有明确的作答引导。
func TestBuildAISearchPromptCoversEverySupportedQuestionType(t *testing.T) {
	cases := []struct {
		questionType int
		expected     string
	}{
		{0, "题型：单选题"},
		{1, "题型：多选题"},
		{2, "该题型在调用侧同时承载填空题、连线题、匹配题"},
		{3, "题型：判断题"},
		{4, "请直接返回覆盖全部小问的最终文本答案"},
		{-4, "这是大作业或综合作答题"},
		{15, "这是阅读理解题"},
	}

	for _, testCase := range cases {
		prompt := buildAISearchPrompt(model.SearchRequest{Question: "题目", Type: testCase.questionType})
		if !strings.Contains(prompt, testCase.expected) {
			t.Fatalf("expected type %d prompt %q to contain %q", testCase.questionType, prompt, testCase.expected)
		}
	}
}

// 回归：客户端把单选题标成 type=3 且带 A-D 选项时，提示词按单选题提问。
// 否则模型会回答「正确/错误」，而选项里没有这两个值 → 空答案（线上 26 例）。
func TestBuildAISearchPromptCorrectsJudgementTypeWithOptionList(t *testing.T) {
	optionPrompt := buildAISearchPrompt(model.SearchRequest{
		Question: "题目",
		Type:     3,
		Options:  []string{"A. 甲", "B. 乙", "C. 丙", "D. 丁"},
	})
	if !strings.Contains(optionPrompt, "题型：单选题") {
		t.Fatalf("expected A-D options under type=3 to be prompted as single choice, got %q", optionPrompt)
	}

	judgementPrompt := buildAISearchPrompt(model.SearchRequest{
		Question: "题目",
		Type:     3,
		Options:  []string{"A. 正确", "B. 错误"},
	})
	if !strings.Contains(judgementPrompt, "题型：判断题") {
		t.Fatalf("expected judgement options to stay judgement type, got %q", judgementPrompt)
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
