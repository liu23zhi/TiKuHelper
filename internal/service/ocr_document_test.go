package service

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
)

func ocrDocumentFixture(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestExtractOCRDocumentImagesValidatesDOCXAndDeduplicatesImages(t *testing.T) {
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	content := ocrDocumentFixture(t, map[string][]byte{"[Content_Types].xml": []byte("<Types/>"), "word/document.xml": []byte("<document/>"), "word/media/image1.png": picture.Bytes(), "word/media/image2.png": picture.Bytes(), "word/embeddings/other.bin": []byte("not image")})
	items, err := ExtractOCRDocumentImages("sample.docx", content)
	if err != nil || len(items) != 1 || items[0].ContentType != "image/png" || !bytes.Equal(items[0].Data, picture.Bytes()) {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if renamed, err := ExtractOCRDocumentImages("renamed.doc", content); err != nil || len(renamed) != 1 {
		t.Fatalf("ZIP DOC content rejected: %v", err)
	}
	for name, data := range map[string][]byte{"legacy.doc": {0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, "bad.docx": []byte("invalid zip"), "not-word.docx": ocrDocumentFixture(t, map[string][]byte{"word/media/a.png": picture.Bytes()}), "spoofed.docx": ocrDocumentFixture(t, map[string][]byte{"[Content_Types].xml": []byte("<Types/>"), "word/document.xml": []byte("ok"), "word/media/a.png": []byte("not image")})} {
		if _, err := ExtractOCRDocumentImages(name, data); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	if _, err := ExtractOCRDocumentImages("legacy.doc", []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}); err == nil || !strings.Contains(err.Error(), "另存为 DOCX") {
		t.Fatalf("legacy DOC guidance missing: %v", err)
	}
	tooMany := map[string][]byte{"[Content_Types].xml": []byte("<Types/>"), "word/document.xml": []byte("<document/>")}
	for i := 0; i < 51; i++ {
		tooMany[fmt.Sprintf("word/media/%d.png", i)] = picture.Bytes()
	}
	if _, err := ExtractOCRDocumentImages("too-many.docx", ocrDocumentFixture(t, tooMany)); err == nil {
		t.Fatal("accepted more than 50 embedded pictures")
	}
	oversized := map[string][]byte{"[Content_Types].xml": []byte("<Types/>"), "word/document.xml": []byte("<document/>"), "word/media/big.png": bytes.Repeat([]byte{0}, OCRUploadMaxBytes+1)}
	if _, err := ExtractOCRDocumentImages("zip-bomb.docx", ocrDocumentFixture(t, oversized)); err == nil {
		t.Fatal("accepted expanded image over budget")
	}
}
