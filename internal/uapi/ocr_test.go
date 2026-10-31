package uapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageOCRUploadsServerDownloadedFileWithDocumentedFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer uapi-test" {
			t.Fatalf("authorization = %q", got)
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content type = %q: %v", r.Header.Get("Content-Type"), err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		fields := map[string]string{}
		fileBytes := []byte(nil)
		fileName := ""
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				t.Fatal(nextErr)
			}
			body, readErr := io.ReadAll(part)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if part.FormName() == "file" {
				fileBytes = body
				fileName = part.FileName()
			} else {
				fields[part.FormName()] = string(body)
			}
		}
		if fileName != "question.png" || string(fileBytes) != "downloaded-image" {
			t.Fatalf("uploaded file = %q %q", fileName, fileBytes)
		}
		for name, want := range map[string]string{
			"image_name": "question.png", "need_location": "false",
			"return_markdown": "true", "enable_cls": "true",
		} {
			if fields[name] != want {
				t.Fatalf("field %s = %q, want %q", name, fields[name], want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "识别结果", "plain_text": "识别结果", "need_location": false})
	}))
	defer server.Close()

	result, err := NewClient("uapi-test").SetImageOCRURL(server.URL).ImageOCR(context.Background(), ImageOCRRequest{
		Image:          []byte("downloaded-image"),
		ImageName:      "question.png",
		ReturnMarkdown: true,
		EnableCLS:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "识别结果" || result.PlainText != "识别结果" || result.RawJSON == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestImageOCRRejectsInvalidInputWithoutRequest(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client := NewClient("uapi-test").SetImageOCRURL(server.URL)
	for _, request := range []ImageOCRRequest{
		{},
		{Image: []byte("data"), ImageName: "question.txt"},
		{Image: make([]byte, MaxImageOCRBytes+1), ImageName: "question.png"},
	} {
		if _, err := client.ImageOCR(context.Background(), request); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("request %+v error = %v", request.ImageName, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached server: %d", calls)
	}
}

func TestImageOCRMapsDocumentedErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"code":"FILE_TOO_LARGE","message":"图片大小不能超过 10485760 字节"}`))
	}))
	defer server.Close()
	_, err := NewClient("uapi-test").SetImageOCRURL(server.URL).ImageOCR(context.Background(), ImageOCRRequest{Image: []byte("data"), ImageName: "question.png"})
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "FILE_TOO_LARGE") {
		t.Fatalf("error lost upstream code: %v", err)
	}
}
