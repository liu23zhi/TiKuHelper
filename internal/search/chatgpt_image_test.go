package search

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// makeTransparentFormulaImage 构造一张「全透明背景 + 黑色字形」的公式截图，
// 与超星题库返回的公式图结构一致。
func makeTransparentFormulaImage(width int, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// 背景保持全透明（零值），中间画一条不透明的黑色横线当作字形。
	for x := 2; x < width-2; x++ {
		img.Set(x, height/2, color.RGBA{R: 0, G: 0, B: 0, A: 255})
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func decodeTestPNG(t *testing.T, payload []byte) image.Image {
	t.Helper()

	img, err := png.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return img
}

func decodeDataURLBytes(t *testing.T, dataURL string) []byte {
	t.Helper()

	_, encoded, ok := strings.Cut(dataURL, ",")
	if !ok {
		t.Fatalf("expected a data url, got %q", dataURL)
	}
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("expected a png data url, got %q", dataURL)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode data url payload: %v", err)
	}
	return decoded
}

func TestPrepareAISearchImageAttachmentFlattensTransparentBackgroundToWhite(t *testing.T) {
	original := makeTransparentFormulaImage(60, 24)

	prepared, contentType := prepareAISearchImageAttachment(original, "image/png")
	if contentType != "image/png" {
		t.Fatalf("expected png content type, got %q", contentType)
	}

	img := decodeTestPNG(t, prepared)
	bounds := img.Bounds()

	// 背景像素必须变成不透明白色；否则模型看到的是「黑字黑底」的全黑图。
	r, g, b, a := img.At(0, 0).RGBA()
	if a != 0xFFFF || r != 0xFFFF || g != 0xFFFF || b != 0xFFFF {
		t.Fatalf("expected transparent background to flatten to opaque white, got r=%d g=%d b=%d a=%d", r, g, b, a)
	}

	// 公式笔画仍然必须是深色，说明没有把内容一起刷白。
	midR, midG, midB, midA := img.At(bounds.Dx()/2, bounds.Dy()/2).RGBA()
	if midA != 0xFFFF {
		t.Fatalf("expected glyph pixel to stay opaque, got alpha %d", midA)
	}
	if midR > 0x4000 || midG > 0x4000 || midB > 0x4000 {
		t.Fatalf("expected glyph pixels to stay dark, got r=%d g=%d b=%d", midR, midG, midB)
	}
}

func TestPrepareAISearchImageAttachmentUpscalesTinyFormulaScreenshots(t *testing.T) {
	original := makeTransparentFormulaImage(40, 22)

	prepared, _ := prepareAISearchImageAttachment(original, "image/png")
	source := decodeTestPNG(t, original)
	result := decodeTestPNG(t, prepared)

	if result.Bounds().Dx() <= source.Bounds().Dx() || result.Bounds().Dy() <= source.Bounds().Dy() {
		t.Fatalf("expected tiny formula image to be upscaled, got %v -> %v", source.Bounds(), result.Bounds())
	}
	if result.Bounds().Dx() > aiSearchImageTargetWidth || result.Bounds().Dy() > aiSearchImageTargetHeight {
		t.Fatalf("expected upscale to stay within target bounds, got %v", result.Bounds())
	}
}

func TestPrepareAISearchImageAttachmentLeavesLargeImagesAtOriginalScale(t *testing.T) {
	original := makeTransparentFormulaImage(1200, 600)

	prepared, _ := prepareAISearchImageAttachment(original, "image/png")
	source := decodeTestPNG(t, original)
	result := decodeTestPNG(t, prepared)

	if result.Bounds() != source.Bounds() {
		t.Fatalf("expected large image dimensions to be preserved, got %v want %v", result.Bounds(), source.Bounds())
	}
}

func TestPrepareAISearchImageAttachmentKeepsOpaqueJPEGBytesOnDecodeFailure(t *testing.T) {
	notAnImage := []byte("this is definitely not an image")

	prepared, contentType := prepareAISearchImageAttachment(notAnImage, "image/png")
	if !bytes.Equal(prepared, notAnImage) || contentType != "image/png" {
		t.Fatal("expected undecodable payload to be passed through unchanged")
	}
}

func TestPrepareAISearchImageAttachmentPreservesOpaqueImageColors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 22))
	for y := 0; y < 22; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}

	prepared, _ := prepareAISearchImageAttachment(buf.Bytes(), "image/png")
	result := decodeTestPNG(t, prepared)

	r, g, b, a := result.At(0, 0).RGBA()
	if a != 0xFFFF || r != 0xFFFF || g != 0 || b != 0 {
		t.Fatalf("expected opaque red to survive, got r=%d g=%d b=%d a=%d", r, g, b, a)
	}
}
