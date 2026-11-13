package service

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"
)

// 本文件覆盖 ticket_attachment_image.go 的 6 层校验链与新增格式支持。
//
// 与 ticket_attachment_test.go 的分工：那边测「落库/权限/检测」，这边只测
// **纯函数** safeTicketAttachmentImage 的行为，不建库、不起服务。

// ticketAttachmentWebPSampleBase64 是一张真实的 WebP（75x100，VP8L 无损），
// 取自 golang.org/x/image 的 testdata（gopher-doc.1bpp.lossless.webp）。
//
// 用真实样本而不是手拼字节：手拼的头部能过 DecodeConfig，但过不了完整解码，
// 那样的用例证明不了「真实 WebP 能上传」。
const ticketAttachmentWebPSampleBase64 = "UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA=="

func ticketAttachmentTestWebP(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(ticketAttachmentWebPSampleBase64)
	if err != nil {
		t.Fatalf("decode webp sample: %v", err)
	}
	return raw
}

// ticketAttachmentTestAnimatedWebP 用真实 WebP 的 VP8L 块构造一张 n 帧动图。
//
// 这台仓库没有 WebP 编码器，所以动图样本只能靠拼容器：VP8X(animation flag) +
// ANIM + n × ANMF(内嵌真实的 VP8L 帧数据)。帧数据是真的，因此解码侧会真的跑。
func ticketAttachmentTestAnimatedWebP(t *testing.T, frames int) []byte {
	t.Helper()
	base := ticketAttachmentTestWebP(t)
	var vp8l []byte
	for off := 12; off < len(base); {
		if off+8 > len(base) {
			t.Fatal("bad base webp: truncated chunk header")
		}
		size := int(binary.LittleEndian.Uint32(base[off+4 : off+8]))
		if string(base[off:off+4]) == "VP8L" {
			vp8l = append([]byte(nil), base[off:off+8+size]...)
			break
		}
		off += 8 + size + (size & 1)
	}
	if vp8l == nil {
		t.Fatal("base webp has no VP8L chunk")
	}
	var le [4]byte
	putLE := func(v uint32) []byte {
		binary.LittleEndian.PutUint32(le[:], v)
		return le[:]
	}
	u24 := func(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16)} }

	var body []byte
	body = append(body, 'V', 'P', '8', 'X')
	body = append(body, putLE(10)...)
	body = append(body, 0x02|0x10, 0, 0, 0) // Animation + Alpha flags
	body = append(body, u24(75-1)...)
	body = append(body, u24(100-1)...)
	body = append(body, 'A', 'N', 'I', 'M')
	body = append(body, putLE(6)...)
	body = append(body, 0, 0, 0, 0, 0, 0) // 背景色 + 无限循环
	for i := 0; i < frames; i++ {
		payloadLen := 16 + len(vp8l)
		body = append(body, 'A', 'N', 'M', 'F')
		body = append(body, putLE(uint32(payloadLen))...)
		body = append(body, u24(0)...)     // x
		body = append(body, u24(0)...)     // y
		body = append(body, u24(75-1)...)  // width - 1
		body = append(body, u24(100-1)...) // height - 1
		body = append(body, u24(10)...)    // duration
		body = append(body, 0)             // flags
		body = append(body, vp8l...)
		if payloadLen&1 == 1 {
			body = append(body, 0)
		}
	}
	out := []byte{'R', 'I', 'F', 'F'}
	out = append(out, putLE(uint32(len(body)+4))...)
	out = append(out, 'W', 'E', 'B', 'P')
	out = append(out, body...)
	return out
}

// ticketAttachmentTestGIF 生成一张 n 帧的 GIF（每帧 width x height 纯色）。
func ticketAttachmentTestGIF(t *testing.T, width, height, n int) []byte {
	t.Helper()
	palette := color.Palette{
		color.RGBA{R: 255, A: 255},
		color.RGBA{G: 255, A: 255},
		color.RGBA{B: 255, A: 255},
	}
	anim := &gif.GIF{
		Image:  make([]*image.Paletted, 0, n),
		Delay:  make([]int, 0, n),
		Config: image.Config{ColorModel: palette, Width: width, Height: height},
	}
	for i := 0; i < n; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, width, height), palette)
		for x := 0; x < width; x++ {
			for y := 0; y < height; y++ {
				frame.SetColorIndex(x, y, uint8(i%len(palette)))
			}
		}
		anim.Image = append(anim.Image, frame)
		anim.Delay = append(anim.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// TestTicketAttachmentImageAcceptsExactlySizeLimit 覆盖判据 ①/②：
// 恰好 20 MiB 通过，20 MiB + 1 字节被拒。
//
// 「恰好 20 MiB」用真实 PNG 补零构造：第 1 层按**上传字节数**判定边界，
// 第 6 层重编码会把补的零全剥掉，所以这条同时验证「边界是闭区间」。
func TestTicketAttachmentImageAcceptsExactlySizeLimit(t *testing.T) {
	valid := ticketAttachmentTestPNG(t, 16, 16)

	// 上限写字面量 20<<20 而不是引用 MaxTicketAttachmentBytes：
	// 期望值与被测常量共享同一符号时，常量漂移（例如退回 2 MiB）测试仍然绿
	// —— 实测确实如此，所以这里必须钉死字面量。
	const atLimitBytes = 20 << 20
	atLimit := make([]byte, atLimitBytes)
	copy(atLimit, valid)
	mimeType, stored, err := safeTicketAttachmentImage(atLimit)
	if err != nil {
		t.Fatalf("exactly %d bytes must be accepted, got %v", atLimitBytes, err)
	}
	if mimeType != "image/png" {
		t.Fatalf("mime = %q, want image/png", mimeType)
	}
	if len(stored) > atLimitBytes {
		t.Fatalf("stored bytes %d exceed limit %d", len(stored), atLimitBytes)
	}
	// 补的零必须被第 6 层剥掉，否则「尾部藏数据」还成立。
	if bytes.Count(stored, make([]byte, 64)) > 0 {
		t.Fatal("re-encoded output must drop the appended zero padding")
	}

	overLimit := make([]byte, atLimitBytes+1)
	copy(overLimit, valid)
	if _, _, err := safeTicketAttachmentImage(overLimit); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("20 MiB + 1 byte must be rejected, got %v", err)
	}
}

// TestTicketAttachmentImageAcceptsLargeRealPNG 覆盖判据 ① 的实质部分：
// 一张**真实**约 19.5 MiB 的 PNG（1600x1600、16 位色、不可压缩噪声）
// 必须能上传 —— 旧上限 2 MiB 下它必然被拒。
//
// 之所以要「真实大图」而不只测补零：补零只证明第 1 层的字节计数，
// 证明不了「解码 + 重编码这条 20 MiB 链路不会被自身的大小复检误杀」。
func TestTicketAttachmentImageAcceptsLargeRealPNG(t *testing.T) {
	const side = 1600 // 1600x1600 = 2.56M 像素，仍在 4M 单帧上限内
	img := image.NewNRGBA64(image.Rect(0, 0, side, side))
	// 伪随机填充：让 PNG 无法压缩，从而把编码结果顶到 ~19.5 MiB。
	state := uint32(0x9e3779b9)
	for i := 0; i < len(img.Pix); i += 2 {
		state = state*1664525 + 1013904223
		img.Pix[i] = byte(state >> 24)
		img.Pix[i+1] = byte(state >> 16)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode large png: %v", err)
	}
	if buf.Len() < 17<<20 {
		t.Fatalf("样本不够大，无法证明 2 MiB 旧上限已被解除：%d 字节", buf.Len())
	}
	mimeType, stored, err := safeTicketAttachmentImage(buf.Bytes())
	if err != nil {
		t.Fatalf("large real png must be accepted, got %v", err)
	}
	if mimeType != "image/png" {
		t.Fatalf("mime = %q, want image/png", mimeType)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(stored))
	if err != nil || format != "png" || cfg.Width != side || cfg.Height != side {
		t.Fatalf("stored image must round-trip: format=%q cfg=%+v err=%v", format, cfg, err)
	}
}

// TestTicketAttachmentImageAcceptsGIFAndWebP 覆盖判据 ③：
// GIF 与 WebP 各一张真实样本必须通过，且 mime 正确。
func TestTicketAttachmentImageAcceptsGIFAndWebP(t *testing.T) {
	cases := []struct {
		name     string
		data     []byte
		mimeWant string
		fmtWant  string
	}{
		{name: "gif_static", data: ticketAttachmentTestGIF(t, 32, 32, 1), mimeWant: "image/gif", fmtWant: "gif"},
		{name: "webp_static", data: ticketAttachmentTestWebP(t), mimeWant: "image/webp", fmtWant: "webp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mimeType, stored, err := safeTicketAttachmentImage(tc.data)
			if err != nil {
				t.Fatalf("must accept real %s sample: %v", tc.name, err)
			}
			if mimeType != tc.mimeWant {
				t.Fatalf("mime = %q, want %q", mimeType, tc.mimeWant)
			}
			_, format, err := image.DecodeConfig(bytes.NewReader(stored))
			if err != nil || format != tc.fmtWant {
				t.Fatalf("stored bytes must stay %s: format=%q err=%v", tc.fmtWant, format, err)
			}
			// 格式必须**保留原样**：GIF/WebP 不得被降级成 PNG。
			if mimeType == "image/png" {
				t.Fatal("gif/webp must not be re-encoded into png")
			}
			if ticketAttachmentExtension(mimeType) == ".png" {
				t.Fatalf("extension for %q must not be .png", mimeType)
			}
		})
	}
}

// TestTicketAttachmentImageAnimatedWebP 覆盖判据 ③/⑦ 的 WebP 侧：
// 动图 WebP 必须被接受并**保住全部帧**，帧数 > 500 必须被拒。
//
// WebP 动图不能被 image.Decode 解码（x/image/webp 只出静态图），所以这条同时验证
// 「第 5 层在动图上退化为结构校验」这条路没把正常文件误杀。
func TestTicketAttachmentImageAnimatedWebP(t *testing.T) {
	const frames = 3
	mimeType, stored, err := safeTicketAttachmentImage(ticketAttachmentTestAnimatedWebP(t, frames))
	if err != nil {
		t.Fatalf("animated webp must be accepted, got %v", err)
	}
	if mimeType != "image/webp" {
		t.Fatalf("animated webp mime = %q, want image/webp", mimeType)
	}
	// 帧数必须原样保留：数一下重建结果里的 ANMF 块。
	if got := bytes.Count(stored, []byte("ANMF")); got != frames {
		t.Fatalf("stored ANMF chunks = %d, want %d（动图被丢帧）", got, frames)
	}
	// 超出帧数上限必须被拒。
	over := ticketAttachmentTestAnimatedWebP(t, MaxTicketAttachmentFrames+1)
	if _, _, err := safeTicketAttachmentImage(over); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("%d-frame webp must be rejected, got %v", MaxTicketAttachmentFrames+1, err)
	}
}

// TestTicketAttachmentImagePreservesAnimatedGIF 是「动图不许丢帧」的钉子。
//
// 变异方向：把 GIF 分支改成与静态图一样走 png.Encode → 只剩第一帧，
// 本用例的帧数断言必然变红（这就是「丢动图 = 丢证据」的回归防线）。
func TestTicketAttachmentImagePreservesAnimatedGIF(t *testing.T) {
	const frames = 3
	mimeType, stored, err := safeTicketAttachmentImage(ticketAttachmentTestGIF(t, 24, 24, frames))
	if err != nil {
		t.Fatalf("animated gif must be accepted, got %v", err)
	}
	if mimeType != "image/gif" {
		t.Fatalf("animated gif mime = %q, want image/gif (PNG 会丢帧)", mimeType)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(stored))
	if err != nil {
		t.Fatalf("stored bytes must decode as gif: %v", err)
	}
	if len(decoded.Image) != frames {
		t.Fatalf("frame count = %d, want %d（动图被降级成静态图）", len(decoded.Image), frames)
	}
	if len(decoded.Delay) != frames || decoded.Delay[0] != 10 {
		t.Fatalf("delays must survive re-encoding, got %v", decoded.Delay)
	}
}

// TestTicketAttachmentImageGIFFrameLimit 覆盖判据 ⑦：帧数 > 500 被拒，= 500 通过。
func TestTicketAttachmentImageGIFFrameLimit(t *testing.T) {
	atLimit := ticketAttachmentTestGIF(t, 8, 8, MaxTicketAttachmentFrames)
	if _, _, err := safeTicketAttachmentImage(atLimit); err != nil {
		t.Fatalf("%d frames must be accepted, got %v", MaxTicketAttachmentFrames, err)
	}
	overLimit := ticketAttachmentTestGIF(t, 8, 8, MaxTicketAttachmentFrames+1)
	if _, _, err := safeTicketAttachmentImage(overLimit); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("%d frames must be rejected, got %v", MaxTicketAttachmentFrames+1, err)
	}
}

// TestTicketAttachmentImageGIFAnimationPixelBudget 覆盖「全帧像素总量」这一层：
// 帧数没超（300 ≤ 500）但每帧都不小，总量必须被拦。
//
// 单帧像素上限挡不住它：512x512 = 262144 像素，远小于 4M 单帧上限，
// 但 300 帧累计 78.6M 像素 > 64M 全帧预算。这条用例因此**只**能由全帧预算拦住。
func TestTicketAttachmentImageGIFAnimationPixelBudget(t *testing.T) {
	const frames = 300
	const side = 512
	if int64(frames)*side*side <= MaxTicketAttachmentAnimationPixels {
		t.Fatalf("用例前提不成立：%d 帧 x %dx%d 未超过全帧预算 %d", frames, side, side, MaxTicketAttachmentAnimationPixels)
	}
	data := ticketAttachmentTestGIF(t, side, side, frames)
	if _, _, err := safeTicketAttachmentImage(data); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("total frame pixels over budget must be rejected, got %v", err)
	}
}

// TestTicketAttachmentImageStripsAppendedContentForEachFormat 覆盖判据 ⑥ 的第 6 层，
// 逐格式验证「文件尾追加数据」不会跟着入库。
//
// GIF/WebP 是本次新增格式，它们各自的尾部同样可以藏数据：
// GIF 的 trailer(0x3B) 之后、WebP 的 RIFF 声明长度之外。
func TestTicketAttachmentImageStripsAppendedContentForEachFormat(t *testing.T) {
	const marker = "APPENDED-SECRET-MARKER"
	cases := []struct {
		name string
		data []byte
		// reject=true 表示该格式对尾部追加采取**硬拒**而不是剥离。
		reject bool
	}{
		{name: "png", data: ticketAttachmentTestPNG(t, 24, 24)},
		{name: "jpeg", data: ticketAttachmentTestJPEG(t)},
		{name: "gif", data: ticketAttachmentTestGIF(t, 16, 16, 2)},
		// WebP 只能硬拒：RIFF 的长度字段是文件结构的一部分，追加字节后
		// 文件本身就自相矛盾（声明长度 ≠ 实际长度），没有「安全剥离」的语义。
		{name: "webp", data: ticketAttachmentTestWebP(t), reject: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tainted := append(append([]byte(nil), tc.data...), []byte(marker)...)
			_, stored, err := safeTicketAttachmentImage(tainted)
			if tc.reject {
				if !errors.Is(err, ErrTicketAttachmentInvalid) {
					t.Fatalf("appended content must be rejected for %s, got %v", tc.name, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("appended content must be stripped, not rejected: %v", err)
			}
			if bytes.Contains(stored, []byte(marker)) {
				t.Fatalf("%s: appended marker survived re-encoding", tc.name)
			}
		})
	}
}

// TestTicketAttachmentImageRejectsWebPMetadataChunk 验证 WebP 的「去元数据」等价实现：
// EXIF/XMP 块在重建时被丢弃，而有意义的数据块必须保留。
func TestTicketAttachmentImageRejectsWebPMetadataChunk(t *testing.T) {
	base := ticketAttachmentTestWebP(t)
	mimeType, stored, err := safeTicketAttachmentImage(base)
	if err != nil {
		t.Fatalf("baseline webp must pass: %v", err)
	}
	if mimeType != "image/webp" {
		t.Fatalf("mime = %q", mimeType)
	}
	// 重建后不应残留任何 RIFF 元数据块。
	for _, fourCC := range []string{"EXIF", "XMP ", "ICCP"} {
		if bytes.Contains(stored, []byte(fourCC)) {
			t.Fatalf("metadata chunk %q must be dropped by the rebuild", fourCC)
		}
	}
}

// TestTicketAttachmentImageRejectsWebPStructuralDamage 覆盖 WebP 的结构化拒绝：
// 未知块、RIFF 长度不符、截断。
func TestTicketAttachmentImageRejectsWebPStructuralDamage(t *testing.T) {
	base := ticketAttachmentTestWebP(t)

	unknownChunk := append([]byte(nil), base...)
	// 在 RIFF 头部之后插入一个未知四字符码块（长度 4）。
	chunk := []byte{'Z', 'Z', 'Z', 'Z', 4, 0, 0, 0, 1, 2, 3, 4}
	merged := make([]byte, 0, len(unknownChunk)+len(chunk))
	merged = append(merged, unknownChunk[:12]...)
	merged = append(merged, chunk...)
	merged = append(merged, unknownChunk[12:]...)
	// 修正 RIFF 长度，使它「结构上自洽」，只剩下未知块这一个毛病。
	size := len(merged) - 8
	merged[4], merged[5], merged[6], merged[7] = byte(size), byte(size>>8), byte(size>>16), byte(size>>24)

	badLength := append([]byte(nil), base...)
	badLength[4] ^= 0xFF // 破坏 RIFF 负载长度

	cases := []struct {
		name string
		data []byte
	}{
		{name: "unknown_chunk", data: merged},
		{name: "riff_length_mismatch", data: badLength},
		{name: "truncated", data: base[:len(base)-40]},
		{name: "header_only", data: base[:12]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := safeTicketAttachmentImage(tc.data); !errors.Is(err, ErrTicketAttachmentInvalid) {
				t.Fatalf("malformed webp must be rejected, got %v", err)
			}
		})
	}
}

// TestTicketAttachmentImageRemainingLayersStillFeel 逐层确认 6 层校验各自还在生效。
//
// 每一条都对应一种具体攻击面；任何一层被删掉，本用例都会有一条子用例变红。
func TestTicketAttachmentImageRemainingLayersStillFeel(t *testing.T) {
	// 第 1 层：空。
	if _, _, err := safeTicketAttachmentImage(nil); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("empty must be rejected, got %v", err)
	}
	// 第 2 层：不是图片（DecodeConfig 失败）。
	if _, _, err := safeTicketAttachmentImage([]byte("这不是图片")); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("non-image must be rejected, got %v", err)
	}
	// 第 3 层：格式不在白名单（BMP 是真实图片，但不在接受范围）。
	bmp := []byte{'B', 'M', 0x3A, 0, 0, 0, 0, 0, 0, 0, 0x36, 0, 0, 0, 0x28, 0, 0, 0,
		1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 24, 0, 0, 0, 0, 0, 4, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 0}
	if _, _, err := safeTicketAttachmentImage(bmp); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("bmp must be rejected by the format whitelist, got %v", err)
	}
	// 第 4 层：单边超限 / 像素超限。
	if _, _, err := safeTicketAttachmentImage(ticketAttachmentTestPNG(t, MaxTicketAttachmentEdge+1, 8)); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatal("edge over limit must be rejected")
	}
	if _, _, err := safeTicketAttachmentImage(ticketAttachmentTestPNG(t, 3000, 3000)); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatal("pixel over limit must be rejected")
	}
	// 第 5 层：头部合法但像素数据损坏 —— 只有真正完整解码才能发现。
	broken := ticketAttachmentTestPNG(t, 32, 32)
	broken[len(broken)-20] ^= 0xFF // 破坏 IEND 前的 CRC/数据
	if _, _, err := safeTicketAttachmentImage(broken); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("corrupted pixel data must be rejected by full decode, got %v", err)
	}
}
