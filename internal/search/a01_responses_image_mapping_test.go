package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
	"image/color"
)

// Every socket below is an owned loopback mock. No real attachment/model/tool is used.
func TestA01ResponsesWireImageReferences(t *testing.T) {
	png := a01ReferencePNG(t, color.RGBA{G: 255, A: 255})
	for _, mode := range []string{"ordinary", "deep"} {
		for _, kind := range []string{"failed_first", "HTML_first", "no_image"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				var mu sync.Mutex
				var received []openAIResponsesContentPart
				counts := map[string]int{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					counts[r.URL.Path]++
					mu.Unlock()
					switch r.URL.Path {
					case "/missing.png":
						w.WriteHeader(404)
					case "/plain.txt":
						w.Header().Set("Content-Type", "text/plain")
						fmt.Fprint(w, "NOT_AN_IMAGE")
					case "/material.html":
						w.Header().Set("Content-Type", "text/html")
						fmt.Fprint(w, "<html><body>LOCAL_MATERIAL</body></html>")
					case "/green.png":
						w.Header().Set("Content-Type", "image/png")
						w.Write(png)
					case "/v1/responses":
						var payload struct {
							Input           []json.RawMessage `json:"input"`
							MaxOutputTokens int               `json:"max_output_tokens"`
						}
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Errorf("actual provider JSON: %v", err)
							w.WriteHeader(400)
							return
						}
						if payload.MaxOutputTokens != aiToolMaxOutputTokens {
							t.Errorf("local output cap lost: %d", payload.MaxOutputTokens)
						}
						for _, item := range payload.Input {
							var message struct {
								Role    string          `json:"role"`
								Content json.RawMessage `json:"content"`
							}
							if json.Unmarshal(item, &message) == nil && message.Role == "user" {
								var parts []openAIResponsesContentPart
								if err := json.Unmarshal(message.Content, &parts); err != nil {
									t.Errorf("actual user parts: %v", err)
								}
								mu.Lock()
								received = parts
								mu.Unlock()
							}
						}
						writeAIFinalStream(w)
					default:
						t.Errorf("unexpected mock route %s", r.URL.Path)
						w.WriteHeader(400)
					}
				}))
				defer server.Close()
				paths := []string{"/missing.png", "/green.png"}
				if kind == "HTML_first" {
					paths = []string{"/material.html", "/green.png"}
				}
				if kind == "no_image" {
					paths = []string{"/missing.png", "/plain.txt"}
				}
				urls := make([]string, len(paths))
				for i, p := range paths {
					urls[i] = server.URL + p
				}
				tool := &testAISearchTool{}
				client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "synthetic-A01-key", Model: "synthetic", MaxAISearchModelCalls: 1, MaxAISearchRetries: 1,
					OCRProvider: func(_ context.Context, sourceURL, _, contentType string, _ []byte) (string, error) {
						mu.Lock()
						counts["syntheticOCR"]++
						mu.Unlock()
						if sourceURL != server.URL+"/green.png" || contentType != "image/png" {
							return "", fmt.Errorf("non-image reached synthetic OCR")
						}
						return "SYNTHETIC_GREEN_OCR", nil
					},
				}
				if mode == "deep" {
					client.SearchTool = tool
				}
				req := model.SearchRequest{Question: "local fixture " + strings.Join(urls, " "), Type: 4, DeepThinking: mode == "deep"}
				answer, usage, err := client.SearchAnswerWithUsage(req)
				if err != nil || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
					t.Fatalf("real production wire flow answer=%v err=%v", answer, err)
				}
				mu.Lock()
				parts := append([]openAIResponsesContentPart(nil), received...)
				seen := map[string]int{}
				for p, n := range counts {
					seen[p] = n
				}
				mu.Unlock()
				if seen["/v1/responses"] != 1 {
					t.Fatalf("unexpected model count: %v", seen)
				}
				for _, p := range paths {
					if seen[p] != 1 {
						t.Fatalf("attachment must be fetched exactly once: %v", seen)
					}
				}
				imageIDs := []string{}
				references := []string{}
				notes := []string{}
				notesIndex := -1
				html := false
				for index, part := range parts {
					if part.Type == "input_image" {
						id, ok := a01ReferenceImageID(openAIContentPart{Type: "image_url", ImageURL: &openAIImageURLPart{URL: part.ImageURL}})
						if !ok {
							t.Fatal("actual wire input_image is not decoded synthetic green PNG")
						}
						imageIDs = append(imageIDs, id)
					}
					if strings.HasPrefix(part.Text, "图片对应关系") || strings.HasPrefix(part.Text, "附件状态说明") {
						notes = append(notes, part.Text)
						notesIndex = index
						for _, line := range strings.Split(part.Text, "\n") {
							if strings.HasPrefix(line, "图") && strings.Contains(line, "为：") {
								references = append(references, line)
							}
						}
					}
					if strings.Contains(part.Text, "LOCAL_MATERIAL") {
						html = true
					}
				}
				if len(parts) == 0 || parts[0].Type != "input_text" || parts[0].Text != buildAISearchPrompt(req) || len(notes) != 1 {
					t.Fatalf("original question/attachment annotation lost: parts=%+v notes=%v", parts, notes)
				}
				if len(usage.ModelCalls) != 1 || usage.TotalTokens != 23 || len(usage.SearchCalls) != 0 || len(usage.WebReadCalls) != 0 || len(tool.queries) != 0 {
					t.Fatalf("one-round wire must preserve executed usage23 and never invoke tools: %+v tool=%v", usage, tool.queries)
				}
				if kind == "no_image" {
					if len(imageIDs) != 0 || len(references) != 0 || len(parts) != 2 || notesIndex != 1 || !strings.HasPrefix(notes[0], "附件状态说明（未提供视觉图片") || len(usage.OCRCalls) != 0 || seen["syntheticOCR"] != 0 {
						t.Fatalf("A01_WIRE_PHANTOM_IMAGE: images=%v visualRefs=%v notes=%v OCR=%v", imageIDs, references, notes, usage.OCRCalls)
					}
					if !strings.Contains(notes[0], "图一下载失败："+urls[0]) || !strings.Contains(notes[0], "图二下载失败："+urls[1]) || !strings.Contains(notes[0], "不要仅因缺图返回不确定") {
						t.Fatal("missing-image evidence disappeared")
					}
				} else {
					expected := "图二为：" + server.URL + "/green.png"
					wantParts, wantNotes := 4, 2
					if kind == "HTML_first" {
						wantParts, wantNotes = 5, 3
					}
					if !reflect.DeepEqual(imageIDs, []string{"green"}) || !reflect.DeepEqual(references, []string{expected}) || len(parts) != wantParts || notesIndex != wantNotes || !strings.HasPrefix(notes[0], "图片对应关系（视觉图片按成功下载顺序提供，图号沿用原题顺序") {
						t.Fatalf("A01_WIRE_DELIVERED_ONLY_ORIGINAL_INDEX: images=%v visualRefs=%v notes=%v", imageIDs, references, notes)
					}
					if seen["syntheticOCR"] != 1 || len(usage.OCRCalls) != 1 || usage.OCRCalls[0].ImageURL != urls[1] || usage.OCRCalls[0].Status != "success" || !strings.Contains(parts[len(parts)-1].Text, "图二 OCR：SYNTHETIC_GREEN_OCR") || strings.Contains(parts[len(parts)-1].Text, "图一 OCR：") {
						t.Fatalf("wire OCR source index/actual-call accounting mismatch: %+v", usage.OCRCalls)
					}
					if kind == "HTML_first" {
						if !strings.Contains(notes[0], "附件一已作为文本提供："+urls[0]+"（不是视觉图片）") || strings.Contains(notes[0], "图一下载失败") {
							t.Fatal("successful HTML must be text, not an image or a failed download")
						}
					} else if !strings.Contains(notes[0], "图一下载失败："+urls[0]) {
						t.Fatal("original first-image failure note lost")
					}
				}
				if (kind == "HTML_first") != html {
					t.Fatalf("HTML content preservation mismatch: %t", html)
				}
				t.Logf("A01_UPSTREAM_WIRE kind=%s mode=%s models=1 tokens=23 images=%d visualRefs=%d OCR=%d originalIndexPreserved=true", kind, mode, len(imageIDs), len(references), len(usage.OCRCalls))
			})
		}
	}
}

func TestA01NoClientMeansNoImageReferences(t *testing.T) {
	req := model.SearchRequest{Question: "question https://a01.invalid/absent.png"}
	for _, mode := range []string{"ordinary", "deep"} {
		t.Run(mode, func(t *testing.T) {
			parts := buildAISearchPromptParts(req, nil)
			if mode == "deep" {
				parts = buildAISearchToolPromptParts(context.Background(), req, nil)
			}
			wire := buildAIResponsesContentParts(parts)
			if len(parts) != 1 || len(wire) != 1 || wire[0].Type != "input_text" || !bytes.Equal([]byte(parts[0].Text), []byte(buildAISearchPrompt(req))) {
				t.Fatalf("A01_NIL_CLIENT_PHANTOM: parts=%+v wire=%+v", parts, wire)
			}
		})
	}
}
