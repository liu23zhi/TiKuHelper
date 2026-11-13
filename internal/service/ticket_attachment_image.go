package service

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"

	// 注册 "webp" 解码器，让 image.DecodeConfig / image.Decode 认得 WebP 的真实魔数。
	// 只导入解码侧：x/image 没有 WebP 编码器，所以 WebP 的「第 6 层」不是重编码，
	// 而是下文的 RIFF 结构重建（见 ticketAttachmentRebuildWebP）。
	_ "golang.org/x/image/webp"
)

// 工单图片附件的校验上限。
//
// 与退款凭证（ticket_refund_proof.go，2 MiB）**刻意不同**：用户已拍板工单图片
// 单张 20 MB、接受 jpg/jpeg/png/gif/webp，理由是工单里的截图/动图是「证据」，
// 被压到 2 MiB 会让用户只能上传缩略图。退款凭证是受限单据，维持原上限。
const (
	MaxTicketAttachmentBytes = 20 << 20
	// MaxTicketAttachmentEdge 与 MaxTicketAttachmentPixels 一起限制解码后的位图规模，
	// 防止「小文件解压成巨图」的压缩炸弹：PNG 可以对纯色大图压到几十 KB。
	MaxTicketAttachmentEdge   = 4096
	MaxTicketAttachmentPixels = 4_000_000

	// MaxTicketAttachmentFrames 限制动图总帧数。动图必须**保留原格式**（改成 PNG
	// 就只剩第一帧，等于丢证据），而保留原格式就意味着要完整解码每一帧，
	// 帧数因此成为内存成本的主要乘数。
	MaxTicketAttachmentFrames = 500
	// MaxTicketAttachmentAnimationPixels 是动图**全部帧**的像素总量上限。
	//
	// 单帧像素上限（MaxTicketAttachmentPixels）挡不住「很多帧 × 每帧都不大」：
	// gif.DecodeAll 会把所有帧同时留在内存里，500 帧 × 4M 像素 = 20 亿像素，
	// 那是 GB 级分配。这里按「调色板帧约 1 字节/像素」折算，64M 像素 ≈ 64 MiB，
	// 与单张 20 MB 的文件上限同一量级，等价于「16 张满像素帧」。
	MaxTicketAttachmentAnimationPixels int64 = 64_000_000
)

var (
	ErrTicketAttachmentInvalid   = errors.New("附件必须是有效的 PNG/JPEG/GIF/WebP 图片，且不超过 20 MiB")
	ErrTicketAttachmentNotFound  = errors.New("附件不存在")
	ErrTicketAttachmentHidden    = errors.New("该图片包含敏感内容，已隐藏")
	ErrTicketAttachmentForbidden = errors.New("无权访问该附件")
)

// errTicketAttachmentStructure 只是内部哨兵，对外一律折成 ErrTicketAttachmentInvalid
// （不把「你的 WebP 哪一段坏了」这种结构性信息回给上传者）。
var errTicketAttachmentStructure = errors.New("invalid image structure")

// safeTicketAttachmentImage 校验并**重新编码**（或等价重建）一张待入库的图片。
//
// 校验链（顺序即防御顺序，每一环都对应一种具体的攻击面）：
//
//  1. 非空且 ≤ 20 MiB —— 挡住超大文件撑爆内存/数据库行（密文也要能整行放下）。
//  2. image.DecodeConfig 只读文件头得到格式与尺寸 —— 不解码像素，先做成本极低的初筛。
//  3. 格式白名单只允许 jpeg/png/gif/webp —— 拒绝 SVG（可内联脚本）以及「扩展名是 .jpg、
//     内容其实是 zip」的伪装：这里判的是**真实魔数**，与上传时声明的文件名无关。
//  4. 宽高与像素总量上限 —— 防压缩炸弹；动图额外加上帧数与全帧像素总量（见下方注释）。
//  5. image.Decode / gif.DecodeAll **完整解码**，并要求解码出的格式与 DecodeConfig 一致 ——
//     防止「头部声称 PNG、后面接别的数据」的多态文件。
//  6. 重新编码（GIF 走 gif.EncodeAll 全帧重编码，WebP 走 RIFF 结构重建）后再次校验大小 ——
//     消除 EXIF/ICC/注释块等嵌入元数据，以及文件尾追加的任意内容
//     （PNG 的 IEND 之后、JPEG 的 EOI 之后、GIF 的 trailer 之后都可以藏数据）。
//     **动图不在这里降级成 PNG**：那会丢掉除第一帧外的所有帧，等于删证据。
//
// 返回的 mimeType 与字节是**净化后**的结果，落库的是它，不是原始上传字节。
func safeTicketAttachmentImage(data []byte) (string, []byte, error) {
	// 第 1 层：非空 + 字节上限。
	if len(data) == 0 || len(data) > MaxTicketAttachmentBytes {
		return "", nil, ErrTicketAttachmentInvalid
	}
	// 第 2 层：只读头。格式与尺寸都来自真实魔数。
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", nil, ErrTicketAttachmentInvalid
	}
	// 第 3 层：格式白名单。逐条字面量列出，新增格式必须显式写在这里。
	switch format {
	case "jpeg", "png", "gif", "webp":
	default:
		return "", nil, ErrTicketAttachmentInvalid
	}
	// 第 4 层：画布尺寸与像素总量（动图的帧级限制在各自的格式分支里做，需要先解析帧表）。
	if cfg.Width > MaxTicketAttachmentEdge || cfg.Height > MaxTicketAttachmentEdge ||
		int64(cfg.Width)*int64(cfg.Height) > MaxTicketAttachmentPixels {
		return "", nil, ErrTicketAttachmentInvalid
	}
	// 第 5 层 + 第 6 层：完整解码 → 净化重建 → 复检大小。
	switch format {
	case "gif":
		return ticketAttachmentReencodeGIF(data)
	case "webp":
		return ticketAttachmentRebuildWebP(data, cfg)
	}
	return ticketAttachmentReencodeStatic(data, format)
}

// ticketAttachmentReencodeStatic 处理 jpeg/png：完整解码后重新编码。
func ticketAttachmentReencodeStatic(data []byte, format string) (string, []byte, error) {
	// 第 5 层：完整解码，并要求解码格式与头部一致（多态文件在这里暴露）。
	img, decoded, err := image.Decode(bytes.NewReader(data))
	if err != nil || decoded != format {
		return "", nil, ErrTicketAttachmentInvalid
	}
	// 第 6 层：重编码，剥离元数据与尾部追加内容。
	var out bytes.Buffer
	mimeType := "image/png"
	if format == "jpeg" {
		mimeType = "image/jpeg"
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 92})
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil || out.Len() == 0 || out.Len() > MaxTicketAttachmentBytes {
		return "", nil, ErrTicketAttachmentInvalid
	}
	return mimeType, out.Bytes(), nil
}

// ticketAttachmentReencodeGIF 处理 GIF：全帧解码后用 gif.EncodeAll 重新编码。
//
// 为什么不把 GIF 转成 PNG：静态 GIF 转 PNG 是等价变换，但**动图会只剩第一帧**，
// 而工单里的动图本身就是证据（例如「点这里之后页面才崩」的操作录屏）。
// gif.EncodeAll 是逐帧无损的，因此「保留动图」与「第 6 层剥离」并不冲突：
// 重编码后注释扩展块、尾部追加字节都会消失。
func ticketAttachmentReencodeGIF(data []byte) (string, []byte, error) {
	// 先做帧级预检（第 4 层延伸）：必须在 DecodeAll **之前**，否则巨型动图已经分配完了。
	frames, totalPixels, err := ticketAttachmentGIFStats(data)
	if err != nil {
		return "", nil, ErrTicketAttachmentInvalid
	}
	if frames <= 0 || frames > MaxTicketAttachmentFrames || totalPixels > MaxTicketAttachmentAnimationPixels {
		return "", nil, ErrTicketAttachmentInvalid
	}
	// 第 5 层：完整解码（同时校验我们数出来的帧数与真实解码结果一致）。
	decoded, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(decoded.Image) != frames || len(decoded.Delay) != frames {
		return "", nil, ErrTicketAttachmentInvalid
	}
	var pixels int64
	for _, frame := range decoded.Image {
		b := frame.Bounds()
		if b.Dx() <= 0 || b.Dy() <= 0 ||
			b.Dx() > MaxTicketAttachmentEdge || b.Dy() > MaxTicketAttachmentEdge {
			return "", nil, ErrTicketAttachmentInvalid
		}
		pixels += int64(b.Dx()) * int64(b.Dy())
	}
	if pixels > MaxTicketAttachmentAnimationPixels {
		return "", nil, ErrTicketAttachmentInvalid
	}
	// 第 6 层：全帧重编码（保留帧序列、延时、循环次数与 disposal）。
	anim := gif.GIF{
		Image: decoded.Image, Delay: decoded.Delay, LoopCount: decoded.LoopCount,
		Disposal: decoded.Disposal, Config: decoded.Config,
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, &anim); err != nil ||
		out.Len() == 0 || out.Len() > MaxTicketAttachmentBytes {
		return "", nil, ErrTicketAttachmentInvalid
	}
	return "image/gif", out.Bytes(), nil
}

// ticketAttachmentGIFStats 结构性统计 GIF 的帧数与全帧像素总量，不分配像素。
//
// 之所以不用 gif.DecodeAll 来数帧：DecodeAll 会为每一帧分配位图，
// 「先解码再判断超限」等于先让炸弹炸一次。这里只走块结构。
func ticketAttachmentGIFStats(data []byte) (int, int64, error) {
	// 头 6 字节（GIF87a/GIF89a）+ 逻辑屏幕描述符 7 字节。
	if len(data) < 13 {
		return 0, 0, errTicketAttachmentStructure
	}
	if string(data[0:3]) != "GIF" {
		return 0, 0, errTicketAttachmentStructure
	}
	packed := data[10]
	off := 13
	if packed&0x80 != 0 {
		// 全局调色板：2^(N+1) 项 × 3 字节。
		off += 3 * (1 << ((packed & 0x07) + 1))
	}
	frames := 0
	var pixels int64
	for off < len(data) {
		switch data[off] {
		case 0x3B: // trailer：文件到此结束，后面的字节由第 6 层重编码丢弃。
			return frames, pixels, nil
		case 0x21: // 扩展块：label + 若干 sub-block
			if off+2 > len(data) {
				return 0, 0, errTicketAttachmentStructure
			}
			off += 2
			next, err := ticketAttachmentGIFSkipSubBlocks(data, off)
			if err != nil {
				return 0, 0, err
			}
			off = next
		case 0x2C: // 图像描述符：9 字节（x,y,w,h,packed）+ 可选局部调色板 + LZW 最小码长 + sub-blocks
			if off+10 > len(data) {
				return 0, 0, errTicketAttachmentStructure
			}
			w := int(data[off+5]) | int(data[off+6])<<8
			h := int(data[off+7]) | int(data[off+8])<<8
			if w <= 0 || h <= 0 || w > MaxTicketAttachmentEdge || h > MaxTicketAttachmentEdge {
				return 0, 0, errTicketAttachmentStructure
			}
			frames++
			pixels += int64(w) * int64(h)
			if frames > MaxTicketAttachmentFrames || pixels > MaxTicketAttachmentAnimationPixels {
				// 提前退出：不必等到文件尾才拒绝。
				return frames, pixels, nil
			}
			local := data[off+9]
			off += 10
			if local&0x80 != 0 {
				off += 3 * (1 << ((local & 0x07) + 1))
			}
			if off >= len(data) {
				return 0, 0, errTicketAttachmentStructure
			}
			off++ // LZW 最小码长
			next, err := ticketAttachmentGIFSkipSubBlocks(data, off)
			if err != nil {
				return 0, 0, err
			}
			off = next
		default:
			return 0, 0, errTicketAttachmentStructure
		}
	}
	// 没有 trailer 的 GIF 是截断文件。
	return 0, 0, errTicketAttachmentStructure
}

// ticketAttachmentGIFSkipSubBlocks 跳过一个「长度前缀 sub-block 序列 + 终止 0 字节」。
func ticketAttachmentGIFSkipSubBlocks(data []byte, off int) (int, error) {
	for {
		if off >= len(data) {
			return 0, errTicketAttachmentStructure
		}
		size := int(data[off])
		off++
		if size == 0 {
			return off, nil
		}
		off += size
		if off > len(data) {
			return 0, errTicketAttachmentStructure
		}
	}
}

// ticketAttachmentRebuildWebP 处理 WebP：解析 RIFF 结构 → 丢弃元数据块 → 原样重建。
//
// 这台仓库没有 WebP 编码器（x/image 只提供解码），所以第 6 层不能靠重编码实现。
// 等价做法是**结构重建**：
//   - RIFF 长度必须精确等于文件长度 ⇒ 文件尾追加的数据一律拒绝（不是「剥掉」，是硬拒）；
//   - 只保留承载像素的块（VP8X/ALPH/VP8 /VP8L/ANIM/ANMF）；
//   - ICCP/EXIF/XMP 这些纯元数据块在重建时丢弃 ⇒ 效果与「重编码去元数据」一致；
//   - 任何未知四字符码 fail-closed，避免把无法解释的数据透传进库。
//
// 动图（VP8X animation bit 置位）无法用 image.Decode 完整解码，因此第 5 层在动图上
// 退化为「逐 ANMF 帧的结构校验」；帧数与全帧像素在重建时累计。
func ticketAttachmentRebuildWebP(data []byte, cfg image.Config) (string, []byte, error) {
	rebuilt, frames, animated, framePixels, err := ticketAttachmentRebuildWebPRiff(data)
	if err != nil {
		return "", nil, ErrTicketAttachmentInvalid
	}
	if !animated {
		// 静态 WebP：帧数 1，像素按画布计，并且第 5 层做完整解码 + 尺寸一致性校验。
		if frames != 1 {
			return "", nil, ErrTicketAttachmentInvalid
		}
		img, decoded, err := image.Decode(bytes.NewReader(data))
		if err != nil || decoded != "webp" {
			return "", nil, ErrTicketAttachmentInvalid
		}
		if img.Bounds().Dx() != cfg.Width || img.Bounds().Dy() != cfg.Height {
			return "", nil, ErrTicketAttachmentInvalid
		}
		framePixels = int64(cfg.Width) * int64(cfg.Height)
	}
	if frames <= 0 || frames > MaxTicketAttachmentFrames ||
		framePixels > MaxTicketAttachmentAnimationPixels {
		return "", nil, ErrTicketAttachmentInvalid
	}
	if len(rebuilt) == 0 || len(rebuilt) > MaxTicketAttachmentBytes {
		return "", nil, ErrTicketAttachmentInvalid
	}
	// 重建后的字节必须仍能被识别（防止我们把结构改坏后落库）。
	if _, again, err := image.DecodeConfig(bytes.NewReader(rebuilt)); err != nil || again != "webp" {
		return "", nil, ErrTicketAttachmentInvalid
	}
	return "image/webp", rebuilt, nil
}

// ticketAttachmentRebuildWebPRiff 是本文件的 RIFF 解析器。
// 返回重建后的整段文件、帧数、是否动图、全帧像素总和。
func ticketAttachmentRebuildWebPRiff(data []byte) ([]byte, int, bool, int64, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, 0, false, 0, errTicketAttachmentStructure
	}
	// RIFF 声明的负载长度必须与真实长度精确相等。差一个字节都判非法：
	// 多出来的是追加数据（藏东西的地方），少了的是截断文件。
	if int64(binary.LittleEndian.Uint32(data[4:8]))+8 != int64(len(data)) {
		return nil, 0, false, 0, errTicketAttachmentStructure
	}
	payload := data[12:]
	body := make([]byte, 0, len(payload))
	frames := 0
	var framePixels int64
	animated := false
	for off := 0; off < len(payload); {
		if off+8 > len(payload) {
			return nil, 0, false, 0, errTicketAttachmentStructure
		}
		fourCC := string(payload[off : off+4])
		size := int64(binary.LittleEndian.Uint32(payload[off+4 : off+8]))
		chunkEnd := int64(off+8) + size
		if size < 0 || chunkEnd > int64(len(payload)) {
			return nil, 0, false, 0, errTicketAttachmentStructure
		}
		chunk := payload[off+8 : chunkEnd]
		switch fourCC {
		case "VP8X":
			if len(chunk) != 10 {
				return nil, 0, false, 0, errTicketAttachmentStructure
			}
			// bit1 = Animation, bit2 = XMP, bit3 = EXIF, bit4 = Alpha, bit5 = ICC。
			animated = chunk[0]&0x02 != 0
		case "ANIM":
			// 6 字节：背景色 4 + 循环次数 2。
			if len(chunk) != 6 {
				return nil, 0, false, 0, errTicketAttachmentStructure
			}
		case "ANMF":
			// 16 字节帧头 + 子块；帧宽高在第 7..12 字节（各 3 字节，减一存储）。
			if len(chunk) < 16 {
				return nil, 0, false, 0, errTicketAttachmentStructure
			}
			w := int64(ticketAttachmentReadU24(chunk[6:9])) + 1
			h := int64(ticketAttachmentReadU24(chunk[9:12])) + 1
			if w <= 0 || h <= 0 || w > MaxTicketAttachmentEdge || h > MaxTicketAttachmentEdge {
				return nil, 0, false, 0, errTicketAttachmentStructure
			}
			frames++
			framePixels += w * h
		case "VP8 ", "VP8L", "ALPH":
			// 静态图的数据块：一次只有一组，帧数由静态分支固定为 1。
			if !animated {
				frames = 1
			}
		case "ICCP", "EXIF", "XMP ":
			// 纯元数据：重建时丢弃（= 重编码带来的「去元数据」效果）。
			off = int(chunkEnd + size&1)
			continue
		default:
			// 无法解释的块不做透传。
			return nil, 0, false, 0, errTicketAttachmentStructure
		}
		body = append(body, payload[off:chunkEnd]...)
		if size&1 == 1 {
			body = append(body, 0) // RIFF 要求块长度为奇数时补一个 pad 字节
		}
		off = int(chunkEnd + size&1)
	}
	out := make([]byte, 0, 12+len(body))
	out = append(out, 'R', 'I', 'F', 'F')
	var sizeBuf [4]byte
	binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(body)+4))
	out = append(out, sizeBuf[:]...)
	out = append(out, 'W', 'E', 'B', 'P')
	out = append(out, body...)
	return out, frames, animated, framePixels, nil
}

func ticketAttachmentReadU24(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}

// ticketAttachmentMediaTypeAllowed 是「库里存的 media_type 还能不能读」的白名单。
//
// 读取路径独立判定一次的理由：库里的行可能来自旧版本（只有 png/jpeg），
// 也可能被人工改成任意字符串；下载接口不能因为 media_type 变了就把字节当作
// 任意 Content-Type 下发（那是 XSS 面）。
func ticketAttachmentMediaTypeAllowed(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// ticketAttachmentExtension 把校验后的 mimeType 映射成上传给检测接口的文件名后缀。
func ticketAttachmentExtension(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ".png"
}
