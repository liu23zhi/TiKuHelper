package service

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
)

// 工单图片附件的校验上限。
//
// 数值与退款凭证**有意保持一致**（2 MiB / 4096 / 4M 像素）：同一套「浏览器截图
// 能有多大」的经验值，也避免两份实现给出不同答案。区别只在于用途：
// 退款凭证是受限单据，工单附件是普通截图。
const (
	MaxTicketAttachmentBytes = 2 << 20
	// MaxTicketAttachmentEdge 与 MaxTicketAttachmentPixels 一起限制解码后的位图规模，
	// 防止「小文件解压成巨图」的压缩炸弹：PNG 可以对纯色大图压到几十 KB。
	MaxTicketAttachmentEdge   = 4096
	MaxTicketAttachmentPixels = 4_000_000
)

var (
	ErrTicketAttachmentInvalid   = errors.New("附件必须是有效的 PNG/JPEG 图片，且不超过 2 MiB")
	ErrTicketAttachmentNotFound  = errors.New("附件不存在")
	ErrTicketAttachmentHidden    = errors.New("该图片包含敏感内容，已隐藏")
	ErrTicketAttachmentForbidden = errors.New("无权访问该附件")
)

// safeTicketAttachmentImage 校验并**重新编码**一张待入库的图片。
//
// 校验链（顺序即防御顺序，每一环都对应一种具体的攻击面）：
//
//  1. 非空且 ≤ 2 MiB —— 挡住超大文件撑爆内存/数据库行（密文也要能整行放下）。
//  2. image.DecodeConfig 只读文件头得到格式与尺寸 —— 不解码像素，先做成本极低的初筛。
//  3. 格式白名单只允许 jpeg/png —— 拒绝 SVG（可内联脚本）、GIF/WebP（本项目前端与
//     下游预览都不保证安全解码），也拒绝「扩展名是 .jpg、内容其实是 zip」的伪装：
//     这里判的是**真实魔数**，与上传时声明的文件名无关。
//  4. 宽高与像素总量上限 —— 防压缩炸弹。
//  5. image.Decode 完整解码，并要求解码出的格式与 DecodeConfig 一致 ——
//     防止「头部声称 PNG、后面接别的数据」的多态文件。
//  6. 重新编码后再次校验大小 —— 消除 EXIF/ICC/注释块等嵌入元数据，以及文件尾
//     追加的任意内容（PNG 的 IEND 之后、JPEG 的 EOI 之后都可以藏数据）。
//
// 返回的 mimeType 与字节是**重新编码后的**结果，落库的是它，不是原始上传字节。
func safeTicketAttachmentImage(data []byte) (string, []byte, error) {
	if len(data) == 0 || len(data) > MaxTicketAttachmentBytes {
		return "", nil, ErrTicketAttachmentInvalid
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") ||
		cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > MaxTicketAttachmentEdge || cfg.Height > MaxTicketAttachmentEdge ||
		int64(cfg.Width)*int64(cfg.Height) > MaxTicketAttachmentPixels {
		return "", nil, ErrTicketAttachmentInvalid
	}
	img, decoded, err := image.Decode(bytes.NewReader(data))
	if err != nil || decoded != format {
		return "", nil, ErrTicketAttachmentInvalid
	}
	// Re-encode, eliminating embedded metadata and appended content.
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

// ticketAttachmentExtension 把校验后的 mimeType 映射成上传给检测接口的文件名后缀。
func ticketAttachmentExtension(mimeType string) string {
	if mimeType == "image/jpeg" {
		return ".jpg"
	}
	return ".png"
}
