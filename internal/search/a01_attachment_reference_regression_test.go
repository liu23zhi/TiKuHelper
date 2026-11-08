package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/pkg/model"
)

// Ordinary CI: go test ./internal/search -run '^TestA01ImageReferenceMatchesDeliveredImages$' -count=1.
// All attachment HTTP uses this closed fake RoundTripper. It never delegates to a transport, DNS, socket, DB, or provider.
type a01ReferenceRoundTrip func(*http.Request) (*http.Response, error)

func (f a01ReferenceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type a01ReferenceBody struct {
	ctx        context.Context
	reader     *bytes.Reader
	allStarted <-chan struct{}
	gate       <-chan struct{}
	done       func()
	once       sync.Once
}

func (b *a01ReferenceBody) Read(p []byte) (int, error) {
	if b.allStarted != nil {
		select {
		case <-b.allStarted:
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
		select {
		case <-b.gate:
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	n, err := b.reader.Read(p)
	if err == io.EOF && b.done != nil {
		b.once.Do(b.done)
	}
	return n, err
}
func (b *a01ReferenceBody) Close() error { return nil }

func a01ReferencePNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 320, 160))
	for y := 0; y < 160; y++ {
		for x := 0; x < 320; x++ {
			im.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func a01ReferenceImageID(p openAIContentPart) (string, bool) {
	if p.Type != "image_url" || p.ImageURL == nil || !strings.HasPrefix(p.ImageURL.URL, "data:image/png;base64,") {
		return "", false
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(p.ImageURL.URL, "data:image/png;base64,"))
	if err != nil {
		return "", false
	}
	im, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	r, g, b, a := im.At(im.Bounds().Min.X, im.Bounds().Min.Y).RGBA()
	switch {
	case r == 0xFFFF && g == 0 && b == 0 && a == 0xFFFF:
		return "red", true
	case r == 0 && g == 0xFFFF && b == 0 && a == 0xFFFF:
		return "green", true
	case r == 0 && g == 0 && b == 0xFFFF && a == 0xFFFF:
		return "blue", true
	default:
		return "", false
	}
}

type a01ReferenceResponse struct {
	kind    string
	payload []byte
	imageID string
}
type a01ReferenceCase struct {
	name                string
	urls                []string
	responses           map[string]a01ReferenceResponse
	wantImageIDs        []string
	wantImageURLs       []string
	wantAttachmentTypes []string
	wantHTML            bool
	reverseCompletion   bool
}
type a01ReferenceObservation struct {
	Schema                          string         `json:"schema"`
	Case                            string         `json:"case"`
	PositiveControls                bool           `json:"positive_controls"`
	ControlFailures                 []string       `json:"control_failures"`
	AllHTTPFakeOnly                 bool           `json:"all_HTTP_fake_only"`
	EachURLRequestedOnce            bool           `json:"each_URL_requested_once"`
	UnexpectedRequestCount          int            `json:"unexpected_request_count"`
	RequestCounts                   map[string]int `json:"request_counts"`
	DownloadCompletion              []string       `json:"download_completion"`
	ImageIDs                        []string       `json:"image_ids"`
	AttachmentPartTypes             []string       `json:"attachment_part_types"`
	TotalPartCount                  int            `json:"total_part_count"`
	ReferencePartIndex              int            `json:"reference_part_index"`
	ReferenceCount                  int            `json:"reference_count"`
	ActualReference                 string         `json:"actual_reference"`
	ExpectedReference               string         `json:"expected_reference"`
	ReferenceMatchesDeliveredImages bool           `json:"reference_matches_delivered_images"`
	ReferenceIsLast                 bool           `json:"reference_is_last"`
	NoSuccessfulImageHasNoReference bool           `json:"no_successful_image_has_no_reference"`
}

func TestA01ImageReferenceMatchesDeliveredImages(t *testing.T) {
	red := a01ReferencePNG(t, color.RGBA{R: 255, A: 255})
	green := a01ReferencePNG(t, color.RGBA{G: 255, A: 255})
	blue := a01ReferencePNG(t, color.RGBA{B: 255, A: 255})
	const failed = "https://a01.invalid/failed.png"
	const page = "https://a01.invalid/material.html"
	const plain = "https://a01.invalid/not-an-image.txt"
	const first = "https://a01.invalid/first.png"
	const second = "https://a01.invalid/second.png"
	const third = "https://a01.invalid/third.png"
	cases := []a01ReferenceCase{
		{name: "failed_first_then_success", urls: []string{failed, second},
			responses:    map[string]a01ReferenceResponse{failed: {kind: "error"}, second: {kind: "image/png", payload: green, imageID: "green"}},
			wantImageIDs: []string{"green"}, wantImageURLs: []string{second}, wantAttachmentTypes: []string{"image_url"}},
		{name: "HTML_then_image", urls: []string{page, first},
			responses:    map[string]a01ReferenceResponse{page: {kind: "text/html", payload: []byte("<html><body><p>A01_OFFLINE_HTML</p></body></html>")}, first: {kind: "image/png", payload: red, imageID: "red"}},
			wantImageIDs: []string{"red"}, wantImageURLs: []string{first}, wantAttachmentTypes: []string{"text", "image_url"}, wantHTML: true},
		{name: "successful_images_reverse_completion", urls: []string{first, second, third},
			responses:    map[string]a01ReferenceResponse{first: {kind: "image/png", payload: red, imageID: "red"}, second: {kind: "image/png", payload: green, imageID: "green"}, third: {kind: "image/png", payload: blue, imageID: "blue"}},
			wantImageIDs: []string{"red", "green", "blue"}, wantImageURLs: []string{first, second, third}, wantAttachmentTypes: []string{"image_url", "image_url", "image_url"}, reverseCompletion: true},
		{name: "no_successful_image", urls: []string{failed, plain},
			responses:    map[string]a01ReferenceResponse{failed: {kind: "error"}, plain: {kind: "text/plain", payload: []byte("A01_NOT_AN_IMAGE")}},
			wantImageIDs: []string{}, wantImageURLs: []string{}, wantAttachmentTypes: []string{}},
	}
	allControls := true
	violations := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			counts := map[string]int{}
			completion := []string{}
			unexpected := 0
			allStarted := make(chan struct{})
			var startedOnce sync.Once
			gates := make([]chan struct{}, len(tc.urls))
			if tc.reverseCompletion {
				for i := range gates {
					gates[i] = make(chan struct{})
				}
				close(gates[len(gates)-1])
			}
			transport := a01ReferenceRoundTrip(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				counts[r.URL.String()]++
				response, ok := tc.responses[r.URL.String()]
				if !ok || r.Method != http.MethodGet || r.URL.Host != "a01.invalid" || r.Header.Get("Referer") != "https://mooc1.chaoxing.com/" {
					unexpected++
					mu.Unlock()
					return nil, errors.New("A01 unexpected closed-fake request")
				}
				if tc.reverseCompletion && len(counts) == len(tc.urls) {
					startedOnce.Do(func() { close(allStarted) })
				}
				mu.Unlock()
				if response.kind == "error" {
					return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("synthetic missing attachment")), Request: r}, nil
				}
				body := &a01ReferenceBody{ctx: r.Context(), reader: bytes.NewReader(response.payload)}
				if tc.reverseCompletion {
					index := -1
					for i, u := range tc.urls {
						if u == r.URL.String() {
							index = i
							break
						}
					}
					if index < 0 {
						return nil, errors.New("A01 unexpected ordinal")
					}
					body.allStarted = allStarted
					body.gate = gates[index]
					body.done = func() {
						mu.Lock()
						completion = append(completion, r.URL.String())
						mu.Unlock()
						if index > 0 {
							close(gates[index-1])
						}
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": []string{response.kind}}, Body: body, ContentLength: int64(len(response.payload)), Request: r}, nil
			})
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("A01 redirects forbidden") }}
			req := model.SearchRequest{Type: 4, Question: "A01 offline synthetic question " + strings.Join(tc.urls, " ")}
			parts := buildAISearchPromptParts(req, client)
			obs := a01ReferenceObservation{Schema: "A01_DELIVERED_IMAGE_REFERENCE_OBSERVATION_V1", Case: tc.name, AllHTTPFakeOnly: true, ControlFailures: []string{}, ImageIDs: []string{}, AttachmentPartTypes: []string{}}
			mu.Lock()
			obs.RequestCounts = map[string]int{}
			for k, v := range counts {
				obs.RequestCounts[k] = v
			}
			obs.DownloadCompletion = append([]string{}, completion...)
			obs.UnexpectedRequestCount = unexpected
			mu.Unlock()
			control := func(ok bool, label string) {
				if !ok {
					obs.ControlFailures = append(obs.ControlFailures, label)
				}
			}
			obs.EachURLRequestedOnce = len(obs.RequestCounts) == len(tc.urls)
			for _, u := range tc.urls {
				if obs.RequestCounts[u] != 1 {
					obs.EachURLRequestedOnce = false
				}
			}
			control(obs.EachURLRequestedOnce, "every extracted URL exactly one fake HTTP request")
			control(obs.UnexpectedRequestCount == 0, "closed fake transport accepts only bound GET URLs and original referer")
			control(len(parts) > 0 && parts[0].Type == "text" && parts[0].Text == buildAISearchPrompt(req), "original first prompt part unchanged")
			htmlSeen := false
			obs.TotalPartCount = len(parts)
			referenceLast := -1
			for i, p := range parts {
				if i == 0 {
					continue
				}
				if p.Type == "text" && strings.HasPrefix(p.Text, "图片对应关系（") {
					obs.ReferenceCount++
					obs.ActualReference = p.Text
					referenceLast = i
					continue
				}
				obs.AttachmentPartTypes = append(obs.AttachmentPartTypes, p.Type)
				if p.Type == "image_url" {
					id, ok := a01ReferenceImageID(p)
					control(ok, "delivered PNG has independently decoded identity")
					obs.ImageIDs = append(obs.ImageIDs, id)
				}
				if p.Type == "text" && strings.Contains(p.Text, "A01_OFFLINE_HTML") {
					htmlSeen = true
				}
			}
			control(reflect.DeepEqual(obs.ImageIDs, tc.wantImageIDs), "successful image parts preserve original input order")
			control(reflect.DeepEqual(obs.AttachmentPartTypes, tc.wantAttachmentTypes), "nonreference attachment part order preserved")
			control(htmlSeen == tc.wantHTML, "HTML is retained as text and not silently dropped")
			if tc.reverseCompletion {
				want := []string{tc.urls[2], tc.urls[1], tc.urls[0]}
				control(reflect.DeepEqual(obs.DownloadCompletion, want), "body EOF completed in forced reverse order")
			}
			if len(tc.wantImageURLs) > 0 {
				var ref strings.Builder
				ref.WriteString("图片对应关系（图片按出现顺序编号，请结合图号理解题目）：")
				numbers := []string{"一", "二", "三"}
				for i, u := range tc.wantImageURLs {
					fmt.Fprintf(&ref, "\n图%s为：%s", numbers[i], u)
				}
				obs.ExpectedReference = ref.String()
				obs.ReferenceMatchesDeliveredImages = obs.ReferenceCount == 1 && obs.ActualReference == obs.ExpectedReference
				obs.ReferenceIsLast = referenceLast == len(parts)-1
				obs.NoSuccessfulImageHasNoReference = true
			} else {
				obs.ReferenceMatchesDeliveredImages = obs.ReferenceCount == 0
				obs.ReferenceIsLast = obs.ReferenceCount == 0
				obs.NoSuccessfulImageHasNoReference = obs.ReferenceCount == 0
			}
			obs.ReferencePartIndex = referenceLast
			obs.PositiveControls = len(obs.ControlFailures) == 0
			if !obs.PositiveControls {
				allControls = false
			}
			valid := obs.ReferenceMatchesDeliveredImages && obs.ReferenceIsLast && obs.NoSuccessfulImageHasNoReference
			if !valid {
				violations++
			}
			raw, err := json.Marshal(obs)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("A01_IMAGE_REFERENCE_OBSERVATION %s", raw)
			if !obs.PositiveControls {
				t.Errorf("A01 fixture positive controls failed: %v", obs.ControlFailures)
			}
			if !valid {
				t.Errorf("A01 delivered image reference mismatch: expected %q; got %q", obs.ExpectedReference, obs.ActualReference)
			}
		})
	}
	t.Logf("A01_IMAGE_REFERENCE_CONTRACT cases=4 positive_controls=%t reference_violations=%d", allControls, violations)
}
