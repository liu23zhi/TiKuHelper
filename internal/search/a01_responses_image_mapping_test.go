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
							Input []json.RawMessage `json:"input"`
						}
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Errorf("actual provider JSON: %v", err)
							w.WriteHeader(400)
							return
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
				client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "synthetic-A01-key", Model: "synthetic", MaxAISearchModelCalls: 1}
				if mode == "deep" {
					client.SearchTool = &testAISearchTool{}
				}
				answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "local fixture " + strings.Join(urls, " "), Type: 4, DeepThinking: mode == "deep"})
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
				html := false
				for _, part := range parts {
					if part.Type == "input_image" {
						id, ok := a01ReferenceImageID(openAIContentPart{Type: "image_url", ImageURL: &openAIImageURLPart{URL: part.ImageURL}})
						if !ok {
							t.Fatalf("actual wire input_image is not decoded synthetic green PNG")
						}
						imageIDs = append(imageIDs, id)
					}
					if strings.HasPrefix(part.Text, "图片对应关系") {
						references = append(references, part.Text)
					}
					if strings.Contains(part.Text, "LOCAL_MATERIAL") {
						html = true
					}
				}
				if kind == "no_image" {
					if len(imageIDs) != 0 || len(references) != 0 {
						t.Fatalf("A01_WIRE_PHANTOM_IMAGE: images=%v references=%v", imageIDs, references)
					}
				} else {
					prefix := "图片对应关系（图片按出现顺序编号，请结合图号理解题目）："
					if mode == "deep" {
						prefix = "图片对应关系（按出现顺序）："
					}
					expected := prefix + "\n图一为：" + server.URL + "/green.png"
					if !reflect.DeepEqual(imageIDs, []string{"green"}) || !reflect.DeepEqual(references, []string{expected}) || parts[len(parts)-1].Text != expected {
						t.Fatalf("A01_WIRE_DELIVERED_ONLY: actual imageIDs=%v references=%v want=%q", imageIDs, references, expected)
					}
				}
				if (kind == "HTML_first") != html {
					t.Fatalf("HTML content preservation mismatch: %t", html)
				}
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
