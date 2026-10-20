package search

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// transparentFormulaPNG 生成一张与题库公式截图结构一致的「透明底 + 黑色字形」PNG。
func transparentFormulaPNG(width int, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 2; x < width-2; x++ {
		img.Set(x, height/2, color.RGBA{R: 0, G: 0, B: 0, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// 端到端（离线）：公式图从下载、预处理、拼装请求体到答案解析与 usage 统计全链路可用。
func TestAISearchPipelineCarriesTransparentFormulaImagesEndToEnd(t *testing.T) {
	var capturedBody map[string]any

	// 同一台测试服务器同时扮演「题库图床」和「OpenAI 兼容上游」。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/star3/origin/") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(transparentFormulaPNG(40, 22))
			return
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		capturedBody = payload

		answer := `{"answers":["H(z)=(1+(1/3)z^-1)/(1-(3/4)z^-1+(1/8)z^-2)，零点 z=-1/3，极点 z=1/2、z=1/4；收敛域 |z|>1/2；h(n)=[(1/2)^n+(2/3)(1/4)^n]u(n)"]}`
		w.Header().Set("Content-Type", "text/event-stream")
		writeAIResponsesTextStream(w, []string{answer}, map[string]any{
			"input_tokens": 2000, "output_tokens": 300, "total_tokens": 2300,
			"input_tokens_details": map[string]any{"cached_tokens": 512},
		})
	}))
	defer server.Close()

	equationURL := server.URL + "/star3/origin/equation.png"
	inputURL := server.URL + "/star3/origin/input.png"
	outputURL := server.URL + "/star3/origin/output.png"
	systemURL := server.URL + "/star3/origin/system.png"

	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "gpt-vision"}
	answers, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{
		Question: "已知离散 LSI 系统的差分方程: " + equationURL +
			" 其中:" + inputURL + "为输入," + outputURL + "为输出 " +
			"1)求系统函数" + systemURL + ",指出系统的零极点; 2)若该系统是因果稳定的,指出系统的收敛域; 3)求该因果稳定系统的单位冲激响应",
		Type: 4,
	})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}

	// 缓存 token 必须被解析出来，供价格表按缓存价计费。
	if usage.CachedInputTokens != 512 {
		t.Fatalf("expected cached input tokens 512, got %+v", usage)
	}

	// 多小问解答必须完整保留，不能被截断或替换成「不确定」。
	if len(answers) != 1 || len(answers[0]) != 1 {
		t.Fatalf("unexpected answer shape: %v", answers)
	}
	answer := answers[0][0]
	for _, fragment := range []string{"极点", "收敛域", "h(n)="} {
		if !strings.Contains(answer, fragment) {
			t.Fatalf("expected answer %q to contain %q", answer, fragment)
		}
	}

	// 请求体必须真的带上 4 张可解码的图片，而不是只有 URL 文本。
	messages, ok := capturedBody["input"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("expected system and user messages, got %+v", capturedBody["messages"])
	}
	userMessage, _ := messages[1].(map[string]any)
	contentParts, ok := userMessage["content"].([]any)
	if !ok {
		t.Fatalf("expected user content parts, got %+v", userMessage["content"])
	}

	inlineImages := 0
	referenceText := ""
	for _, rawPart := range contentParts {
		part, _ := rawPart.(map[string]any)
		switch part["type"] {
		case "input_image":
			dataURL, _ := part["image_url"].(string)
			if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
				t.Fatalf("expected a PNG data url, got prefix %q", dataURL[:min(40, len(dataURL))])
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.SplitN(dataURL, ",", 2)[1])
			if err != nil {
				t.Fatalf("decode inline image: %v", err)
			}
			img, err := png.Decode(bytes.NewReader(decoded))
			if err != nil {
				t.Fatalf("expected inline image to be a valid png: %v", err)
			}
			// 透明底必须已经合成为白底，否则模型看到的是黑底黑字的空图。
			r, g, b, a := img.At(0, 0).RGBA()
			if a != 0xFFFF || r != 0xFFFF || g != 0xFFFF || b != 0xFFFF {
				t.Fatalf("expected flattened white background, got r=%d g=%d b=%d a=%d", r, g, b, a)
			}
			inlineImages++
		case "input_text":
			text, _ := part["text"].(string)
			if strings.Contains(text, "图片对应关系") {
				referenceText = text
			}
		}
	}
	if inlineImages != 4 {
		t.Fatalf("expected 4 inline images in the request body, got %d", inlineImages)
	}

	// 图号对应关系必须写进提示词，否则模型无法知道哪张图是输入、哪张是输出。
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

// 端到端（联网）：真实题库公式图必须变成「白底 + 深色字形」的可读图片。
// 题库图床不可达时跳过，避免外部网络波动导致构建失败。
func TestRealChaoxingFormulaImagesBecomeReadableWhiteBackgroundPNGs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent verification in short mode")
	}

	urls := []string{
		"https://p.ananas.chaoxing.com/star3/origin/3969438ce57a85e4d3fa8d3faaa0bc87.png",
		"https://p.ananas.chaoxing.com/star3/origin/05baca4bfd6fad9af6de874c8e2f809e.png",
		"https://p.ananas.chaoxing.com/star3/origin/3ac3d0da42cf634dedc24d1dafcb70d7.png",
		"https://p.ananas.chaoxing.com/star3/origin/52d1f17e764fcc69058249fe46ba52f2.png",
	}

	question := "已知离散 LSI 系统的差分方程: " + urls[0] +
		" 其中:" + urls[1] + "为输入," + urls[2] + "为输出 " +
		"1)求系统函数" + urls[3] + ",指出系统的零极点; 2)若该系统是因果稳定的,指出系统的收敛域; 3)求该因果稳定系统的单位冲激响应"

	client := (&ChatGptClient{}).getAttachmentHTTPClient().GetClient()
	parts := buildAISearchPromptParts(model.SearchRequest{Question: question, Type: 4}, client)

	// 先统计下载结果：题库图床不可达或部分超时时跳过，避免外部网络波动导致构建失败。
	imageParts := 0
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL != nil {
			imageParts++
		}
	}
	if imageParts != 4 {
		t.Skipf("题库图床当前不可达（已下载 %d/4 张），跳过真实图片校验", imageParts)
	}

	for _, part := range parts {
		if part.Type != "image_url" || part.ImageURL == nil {
			continue
		}

		payload := decodeDataURLBytes(t, part.ImageURL.URL)
		img := decodeTestPNG(t, payload)

		dark, light, total := 0, 0, 0
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				luma := (int(r>>8)*299 + int(g>>8)*587 + int(b>>8)*114) / 1000
				switch {
				case luma < 100:
					dark++
				case luma > 200:
					light++
				}
				total++
			}
		}

		// 全黑或全白都说明字形信息丢了。
		if dark == 0 || light == 0 {
			t.Fatalf("expected both glyph and background pixels, got dark=%d light=%d size=%v", dark, light, img.Bounds())
		}
		if dark*100/total > 60 {
			t.Fatalf("expected mostly white background, got %d%% dark pixels", dark*100/total)
		}
	}
}
