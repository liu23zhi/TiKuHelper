package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// TicketAttachmentMaxPerTicket 限制单个工单的图片数量，避免把工单表撑成图床。
	TicketAttachmentMaxPerTicket = 9
	// TicketAttachmentMaxPerMessage 限制单次回复携带的图片数量。
	TicketAttachmentMaxPerMessage = 6
	// TicketAttachmentScanMaxAttempts 与工单提醒同构：含首次共 3 次尝试。
	TicketAttachmentScanMaxAttempts = 3
	// ticketAttachmentScanTimeout 单次检测外呼的超时。上传路径不同步调用它，
	// 所以这里可以给足时间；超时按「未检测出结论」处理（fail-closed）。
	ticketAttachmentScanTimeout = 30 * time.Second
)

var (
	// ErrTicketAttachmentDisabled 表示加密密钥未配置。加密是落库的前置条件：
	// 没有密钥就不该把用户图片写到磁盘上，因此上传直接失败而不是明文降级。
	ErrTicketAttachmentDisabled = errors.New("工单图片加密未配置")
	ErrTicketAttachmentLimit    = errors.New("图片数量超出限制")
)

// ticketAttachmentCipher 返回附件加密用的 AEAD。
//
// 密钥来源优先级：
//  1. TIKU_TICKET_ATTACHMENT_KEY —— 专用密钥（推荐独立配置）；
//  2. 回退到 TIKU_REFUND_PROOF_KEY —— 复用部署中已有的 32 字节密钥。
//
// 复用同一份密钥材料是**有意的**：两个子系统用不同的 AAD 域前缀
// （tikuhelper-ticket-attachment-v1: 与 tikuhelper-refund-proof-v1:），
// 因此密文无法跨系统互换解密；这样做只为避免「没配新密钥 ⇒ 功能整体不可用」。
func ticketAttachmentCipher() (cipher.AEAD, error) {
	raw := strings.TrimSpace(os.Getenv("TIKU_TICKET_ATTACHMENT_KEY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("TIKU_REFUND_PROOF_KEY"))
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil || len(key) != 32 {
		return nil, ErrTicketAttachmentDisabled
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrTicketAttachmentDisabled
	}
	return cipher.NewGCM(block)
}

// ticketAttachmentAAD 把附件身份绑进密文。
//
// 刻意**不含** ticket_id：图片可能在工单生成之前就上传（「先上传后检测、发工单时带图」），
// 建单时才绑定到工单，若 AAD 含 ticket_id 就必须在绑定那一刻重新加密。
// 换来的保护是：自增 id + 上传者 + 媒体类型被绑定，因此
//   - 把 A 的密文塞进 B 的行（不同 id）→ GCM 校验失败；
//   - 把密文挂到另一个用户的行（不同 user_id）→ 失败；
//   - 只改 media_type 头 → 失败。
//
// 「跨工单搬运」由 SQL 条件（id + user_id + 空 message_id）保证，见 BindTicketAttachmentsInTx。
func ticketAttachmentAAD(id int64, userID int32, mediaType string) []byte {
	return []byte(fmt.Sprintf("tikuhelper-ticket-attachment-v1:%d:%d:%s", id, userID, mediaType))
}

// judgeTicketAttachmentScan 是**唯一的**判定入口（判定规则表见本文件底部注释，
// 下方也为完整规则）。任何新增的上游字段都必须汇总到这里，不要各写一份。
//
// 规则（任一命中即敏感）：
//
//	is_nsfw == true                        → sensitive
//	suggestion ∈ {review, block}           → sensitive
//	risk_level ∈ {medium, high}            → sensitive
//	缺判定字段（上游没给结论）              → failed（不显示）
//	其余（is_nsfw=false + suggestion=pass 或空 + risk_level=low 或空）→ approved
func judgeTicketAttachmentScan(result uapi.ImageNSFWResponse) (string, string) {
	suggestion := strings.ToLower(strings.TrimSpace(result.Suggestion))
	riskLevel := strings.ToLower(strings.TrimSpace(result.RiskLevel))
	if result.IsNSFW == nil && suggestion == "" && riskLevel == "" {
		return entity.TicketAttachmentScanFailed, "检测响应缺少判定字段"
	}
	if result.IsNSFW != nil && *result.IsNSFW {
		return entity.TicketAttachmentScanSensitive, "上游判定为敏感图片"
	}
	switch suggestion {
	case "review", "block":
		return entity.TicketAttachmentScanSensitive, "上游建议 " + suggestion
	}
	switch riskLevel {
	case "medium", "high":
		return entity.TicketAttachmentScanSensitive, "上游风险等级 " + riskLevel
	}
	// 到这里仍要确认上游「说过它正常」：只有在明确给出 is_nsfw=false，或明确给出
	// suggestion=pass/low 风险时才算通过。全是空值走上面的 failed 分支。
	if result.IsNSFW != nil && !*result.IsNSFW {
		return entity.TicketAttachmentScanApproved, ""
	}
	if suggestion == "pass" || riskLevel == "low" {
		return entity.TicketAttachmentScanApproved, ""
	}
	return entity.TicketAttachmentScanFailed, "检测结论不可用"
}

// ticketAttachmentVisible 是展示判据。只有 approved 显示图片，
// 其余状态（pending / scanning / sensitive / failed / 未知值）一律隐藏。
//
// 这是 fail-closed 的**唯一实现位置**：用户端与管理端默认展示都调用它，
// 不要在任何 handler 里另写一套 `scanState == "approved" || ...`。
func ticketAttachmentVisible(attachment entity.TicketAttachment) bool {
	return strings.EqualFold(strings.TrimSpace(attachment.ScanState), entity.TicketAttachmentScanApproved)
}

// TicketAttachmentInfo 是附件的对外表示。它**不含**密文、nonce、原始文件名，
// 也不含检测错误详情：这些都不该进入普通响应。
type TicketAttachmentInfo struct {
	ID        int64  `json:"id"`
	TicketID  int64  `json:"ticketId"`
	MessageID *int64 `json:"messageId,omitempty"`
	MediaType string `json:"mediaType"`
	ByteSize  int64  `json:"byteSize"`
	// Visible 是服务端算好的展示许可。前端**不得**自行用 scanState 推导，
	// 也不得在 Visible=false 时尝试拉取图片。
	Visible bool `json:"visible"`
	// HiddenReason 是给用户看的中文提示；Visible=true 时为空。
	HiddenReason string    `json:"hiddenReason,omitempty"`
	ScanState    string    `json:"scanState"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ticketAttachmentHiddenReason 把内部状态翻译成用户能读的提示。
// sensitive 与 pending/failed 的措辞必须不同：前者是「已判定为敏感」，
// 后者是「还没结论」，把两者混为一谈会让用户以为自己的图片被误判。
func ticketAttachmentHiddenReason(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case entity.TicketAttachmentScanSensitive:
		return "此图片包含敏感内容，已隐藏"
	case entity.TicketAttachmentScanPending, entity.TicketAttachmentScanScanning:
		return "图片正在进行安全检测，暂不显示"
	default:
		return "图片未通过安全检测，已隐藏"
	}
}

func newTicketAttachmentInfo(attachment entity.TicketAttachment) TicketAttachmentInfo {
	visible := ticketAttachmentVisible(attachment)
	info := TicketAttachmentInfo{
		ID: attachment.ID, TicketID: attachment.TicketID, MessageID: attachment.MessageID,
		MediaType: attachment.MediaType, ByteSize: attachment.ByteSize,
		Visible: visible, ScanState: attachment.ScanState, CreatedAt: attachment.CreatedAt,
	}
	if !visible {
		info.HiddenReason = ticketAttachmentHiddenReason(attachment.ScanState)
	}
	return info
}

// ListTicketAttachmentsWithDB 返回一张工单的附件（按创建顺序），并已算好展示许可。
//
// 表不存在时返回空列表而不是报错（表/列可能不存在时先探测再操作）。
// 这让「迁移还没跑」或「老库尚未升级」不至于把工单详情的读取整条打挂 ——
// 附件是附属信息，缺表时等价于「没有附件」。
func ListTicketAttachmentsWithDB(db *gorm.DB, ticketID int64) ([]TicketAttachmentInfo, error) {
	if db == nil || ticketID <= 0 {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameTicketAttachment) {
		return nil, nil
	}
	var rows []entity.TicketAttachment
	if err := db.Where("ticket_id = ?", ticketID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	infos := make([]TicketAttachmentInfo, 0, len(rows))
	for _, row := range rows {
		infos = append(infos, newTicketAttachmentInfo(row))
	}
	return infos, nil
}

// StoreTicketAttachmentWithDB 校验 → 重新编码 → （可选）同步内容审核 → 加密 → 落库。
//
// 异步检测由 ProcessPendingTicketAttachmentScans 后台消费（scan_state='pending'）。
// 内容审核开关打开时，上传路径上还会**同步**先判一次（见下面的三态处置）；
// 同步判违规的图**照常落库**、只是直接置 sensitive，因此不会进入异步队列。
//
// ticketID == 0 表示「建单前先传图」的草稿态：这类附件 ticket_id=0，
// 只有上传者能在建单时用 BindDraftTicketAttachmentsInTx 认领，其他人读不到
// （读取入口都先按工单鉴权，ticket_id=0 不对应任何工单）。
//
// 授权在解码之前完成（先把昂贵的解码工作挡在权限之后）。
func StoreTicketAttachmentWithDB(db *gorm.DB, user *entity.User, ticketID int64, data []byte) (TicketAttachmentInfo, error) {
	if db == nil || user == nil {
		return TicketAttachmentInfo{}, ErrTicketForbidden
	}
	if ticketID < 0 {
		return TicketAttachmentInfo{}, ErrTicketNotFound
	}
	if ticketID > 0 {
		if _, err := ticketForAttachmentWithDB(db, user, ticketID); err != nil {
			return TicketAttachmentInfo{}, err
		}
	}
	aead, err := ticketAttachmentCipher()
	if err != nil {
		return TicketAttachmentInfo{}, err
	}
	// 便宜的检查（数量上限）先做，再解码 PNG/JPEG。
	var count int64
	scope := db.Model(&entity.TicketAttachment{})
	if ticketID > 0 {
		scope = scope.Where("ticket_id = ?", ticketID)
	} else {
		// 草稿区按上传者限额，避免有人把表当图床。
		scope = scope.Where("ticket_id = ? AND user_id = ?", 0, user.ID)
	}
	if err := scope.Count(&count).Error; err != nil {
		return TicketAttachmentInfo{}, err
	}
	if count >= TicketAttachmentMaxPerTicket {
		return TicketAttachmentInfo{}, ErrTicketAttachmentLimit
	}
	mimeType, safe, err := safeTicketAttachmentImage(data)
	if err != nil {
		return TicketAttachmentInfo{}, err
	}
	// 内容审核：**同步**、在落库之前。开关默认关闭（零外呼）。
	//
	// 三态处置（见 TicketImageModerationState）：
	//   pass        → 照常落库，scan_state=pending，异步扫描继续复核
	//   flagged     → **照常落库**，scan_state 直接置 sensitive（用户端隐藏、管理端可见）
	//   unavailable → 拒绝入库（fail-closed）
	//
	// flagged **不拒绝入库**是有意为之：工单系统必须允许用户上传敏感图片与敏感词，
	// 误判时用户仍要能开单说明情况 —— 拦在上传层等于让人无法申辩。拦截的责任
	// 交给展示层（ticketAttachmentVisible 只放行 approved）。
	// 而 unavailable 必须拒绝：那是「审核没跑成」，放行等于把上游打挂就能绕过审核。
	//
	// 审核送的是重编码后的 safe 而不是原始 data：EXIF/尾部藏的东西在
	// safeTicketAttachmentImage 里已经被剥掉，让上游看用户看不到的内容没有意义。
	scanState := entity.TicketAttachmentScanPending
	if TicketImageModerationEnabledWithDB(db) {
		decision := moderateTicketAttachmentImage(context.Background(), db, safe, mimeType)
		switch decision.State {
		case TicketImageModerationFlagged:
			// 落库即打标：不经过 pending，异步 worker 因此不会再来复核这张图
			// （它的取数条件是 scan_state = pending），也就不会把 sensitive 改回 approved。
			scanState = entity.TicketAttachmentScanSensitive
		case TicketImageModerationUnavailable:
			return TicketAttachmentInfo{}, decision.unavailableError()
		}
	}
	digest := sha256.Sum256(safe)

	var created entity.TicketAttachment
	err = db.Transaction(func(tx *gorm.DB) error {
		// 在同一事务里复查数量，避免两个并发上传都通过前置检查后超限。
		var inTx int64
		recheck := tx.Model(&entity.TicketAttachment{})
		if ticketID > 0 {
			recheck = recheck.Where("ticket_id = ?", ticketID)
		} else {
			recheck = recheck.Where("ticket_id = ? AND user_id = ?", 0, user.ID)
		}
		if err := recheck.Count(&inTx).Error; err != nil {
			return err
		}
		if inTx >= TicketAttachmentMaxPerTicket {
			return ErrTicketAttachmentLimit
		}
		// 先插入拿到自增 ID，再用它作为 AAD 加密 —— ID 因此被绑定进密文，
		// 把一行密文搬到另一行必然解密失败。
		created = entity.TicketAttachment{
			TicketID: ticketID, UserID: user.ID, MediaType: mimeType,
			ByteSize: int64(len(safe)), SHA256: hex.EncodeToString(digest[:]),
			ScanState: scanState,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		ciphertext := aead.Seal(nil, nonce, safe, ticketAttachmentAAD(created.ID, created.UserID, created.MediaType))
		return tx.Model(&entity.TicketAttachment{}).Where("id = ?", created.ID).Updates(map[string]interface{}{
			"nonce": nonce, "ciphertext": ciphertext,
		}).Error
	})
	if err != nil {
		return TicketAttachmentInfo{}, err
	}
	created.Nonce = nil
	return newTicketAttachmentInfo(created), nil
}

// BindDraftTicketAttachmentsInTx 在建单事务里把「建单前上传的草稿图」认领到新工单。
//
// 条件必须是 ticket_id = 0 AND user_id = 当前用户：既防止别人的草稿图被抢走，
// 也保证一张草稿图只能被认领一次（认领后 ticket_id 不再是 0）。
func BindDraftTicketAttachmentsInTx(tx *gorm.DB, user *entity.User, ticketID, messageID int64, ids []int64) error {
	if tx == nil || len(ids) == 0 {
		return nil
	}
	if len(ids) > TicketAttachmentMaxPerMessage {
		return ErrTicketAttachmentLimit
	}
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return ErrTicketAttachmentNotFound
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	result := tx.Model(&entity.TicketAttachment{}).
		Where("id IN ? AND ticket_id = ? AND user_id = ?", unique, 0, user.ID).
		Updates(map[string]interface{}{"ticket_id": ticketID, "message_id": messageID})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != int64(len(unique)) {
		return ErrTicketAttachmentNotFound
	}
	return nil
}

// ticketForAttachmentWithDB 复用工单的可见性规则：普通用户只能给自己名下的工单
// 加图，管理员可以替任何工单加图（代客处理）。
//
// ticketID == 0（尚未建单的草稿图）在这里一律视为不存在：草稿图只能由上传者
// 在建单事务里认领，不能通过读取接口访问。
func ticketForAttachmentWithDB(db *gorm.DB, user *entity.User, ticketID int64) (entity.Ticket, error) {
	if ticketID <= 0 {
		return entity.Ticket{}, ErrTicketAttachmentNotFound
	}
	query := db.Where("id = ?", ticketID)
	if !isTicketAdmin(user) {
		query = query.Where("user_id = ?", user.ID)
	}
	var ticket entity.Ticket
	if err := query.First(&ticket).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.Ticket{}, ErrTicketNotFound
		}
		return entity.Ticket{}, err
	}
	return ticket, nil
}

// BindTicketAttachmentsInTx 把「已上传但还没挂到消息上」的附件绑到新消息。
//
// 只允许绑定**同一工单、同一上传者、message_id 为空**的附件：
// 否则 A 用户可以把 B 用户的图片挂到自己的回复下，或把一张图重复挂到多条消息。
func BindTicketAttachmentsInTx(tx *gorm.DB, user *entity.User, ticketID, messageID int64, ids []int64) error {
	if tx == nil || len(ids) == 0 {
		return nil
	}
	if len(ids) > TicketAttachmentMaxPerMessage {
		return ErrTicketAttachmentLimit
	}
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return ErrTicketAttachmentNotFound
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	result := tx.Model(&entity.TicketAttachment{}).
		Where("id IN ? AND ticket_id = ? AND user_id = ? AND message_id IS NULL", unique, ticketID, user.ID).
		Update("message_id", messageID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != int64(len(unique)) {
		// 有一条没绑上就整体回滚（调用方在事务里），不能静默吞掉半绑定。
		return ErrTicketAttachmentNotFound
	}
	return nil
}

// ReadTicketAttachmentWithDB 读取附件字节。
//
// 权限与展示解耦：
//   - 普通用户：只能读自己工单内的附件，且**必须** visible（否则返回 ErrTicketAttachmentHidden，
//     不返回任何字节 —— 这是需求里的「直接拒绝显示」在服务端的落点）；
//   - 管理员：可以读任何工单的附件（含敏感图），但调用方必须写审计。
//
// 返回值 allowSensitive 告诉 handler「这次是否绕过了展示判定」，用于决定是否记审计。
func ReadTicketAttachmentWithDB(db *gorm.DB, user *entity.User, attachmentID int64) (mimeType string, data []byte, allowSensitive bool, err error) {
	if db == nil || user == nil {
		return "", nil, false, ErrTicketForbidden
	}
	if attachmentID <= 0 {
		return "", nil, false, ErrTicketAttachmentNotFound
	}
	var attachment entity.TicketAttachment
	if err := db.Where("id = ?", attachmentID).First(&attachment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil, false, ErrTicketAttachmentNotFound
		}
		return "", nil, false, err
	}
	if _, err := ticketForAttachmentWithDB(db, user, attachment.TicketID); err != nil {
		return "", nil, false, err
	}
	if !ticketAttachmentVisible(attachment) && !isTicketAdmin(user) {
		return "", nil, false, ErrTicketAttachmentHidden
	}
	aead, cipherErr := ticketAttachmentCipher()
	if cipherErr != nil {
		return "", nil, false, cipherErr
	}
	if attachment.MediaType != "image/png" && attachment.MediaType != "image/jpeg" {
		return "", nil, false, ErrTicketAttachmentInvalid
	}
	if len(attachment.Nonce) != aead.NonceSize() ||
		len(attachment.Ciphertext) > MaxTicketAttachmentBytes+aead.Overhead() {
		return "", nil, false, ErrTicketAttachmentInvalid
	}
	plain, openErr := aead.Open(nil, attachment.Nonce, attachment.Ciphertext,
		ticketAttachmentAAD(attachment.ID, attachment.UserID, attachment.MediaType))
	if openErr != nil {
		return "", nil, false, ErrTicketAttachmentInvalid
	}
	return attachment.MediaType, plain, !ticketAttachmentVisible(attachment), nil
}

// TicketAttachmentScanResult 汇总一次检测的落库字段。
type TicketAttachmentScanResult struct {
	State      string
	NSFWScore  *float64
	RiskLevel  string
	Suggestion string
	Label      string
	CheckError string
}

// scanTicketAttachmentOnce 执行一次检测（不重试；重试由 worker 的 attempt_count 负责）。
//
// 任何错误都返回 State=failed 的结果而不是零值：调用方把结果写进库里，
// 图片因此保持隐藏。**没有 key** 也走这条路径，这就是「无 API key 按敏感处理」。
func scanTicketAttachmentOnce(ctx context.Context, db *gorm.DB, attachmentID int64, plain []byte, mimeType string) TicketAttachmentScanResult {
	failed := func(reason string) TicketAttachmentScanResult {
		return TicketAttachmentScanResult{State: entity.TicketAttachmentScanFailed, CheckError: truncateUTF8(reason, 500)}
	}
	key := strings.TrimSpace(GetUAPIAPIKeyWithDB(db))
	if !strings.HasPrefix(key, "uapi-") || len(key) <= len("uapi-") {
		// 没配置密钥：不假装成功，也不浪费一次必然 401 的外呼。
		return failed("未配置 UapiPro 密钥，无法完成敏感图片检测")
	}
	client := uapi.NewClient(key).SetTimeout(ticketAttachmentScanTimeout).SetMaxRetries(0)
	if override := strings.TrimSpace(ticketAttachmentNSFWURLOverride); override != "" {
		client.SetImageNSFWURL(override)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, ticketAttachmentScanTimeout)
	defer cancel()
	result, err := client.ImageNSFW(callCtx, uapi.ImageNSFWRequest{
		Image:     plain,
		ImageName: "ticket-attachment" + ticketAttachmentExtension(mimeType),
	})
	if err != nil {
		// 上游故障 / 超时 / 解析失败：一律 failed（不显示），绝不放过。
		return failed("敏感图片检测失败：" + err.Error())
	}
	state, reason := judgeTicketAttachmentScan(result)
	scan := TicketAttachmentScanResult{
		State: state, NSFWScore: result.NSFWScore, RiskLevel: strings.ToLower(strings.TrimSpace(result.RiskLevel)),
		Suggestion: strings.ToLower(strings.TrimSpace(result.Suggestion)), Label: strings.TrimSpace(result.Label),
	}
	if state == entity.TicketAttachmentScanSensitive {
		// 只记判定结论与分数；原始响应体可能含模型细节，且对排障没有额外价值。
		logger.SysLog(fmt.Sprintf("ticket_attachment_sensitive attachmentId=%d reason=%s nsfwScore=%v", attachmentID, reason, result.NSFWScore))
	}
	if state == entity.TicketAttachmentScanFailed {
		scan.CheckError = truncateUTF8(reason, 500)
	}
	return scan
}

// ticketAttachmentNSFWURLOverride 只给测试注入 httptest 地址用；
// 生产保持空，走 uapi 的文档完整地址。
var ticketAttachmentNSFWURLOverride string

// ProcessPendingTicketAttachmentScans 是后台消费入口：取一批 pending 且到达重试时间的
// 附件，逐条 claim → 解密 → 检测 → 写结果。
//
// 与 ticket_notifications 的做法一致：claim 用「状态 + attempt_count + updated_at」
// 乐观并发，避免两个 worker 重复检测同一张图（重复检测会重复计费）。
func ProcessPendingTicketAttachmentScans(ctx context.Context, db *gorm.DB, now time.Time) (int, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	if ctx.Err() != nil {
		return 0, nil
	}
	db = db.WithContext(dbqueue.WithPriority(ctx, dbqueue.PriorityLow))
	var rows []entity.TicketAttachment
	if err := db.Where("scan_state = ? AND attempt_count < ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)",
		entity.TicketAttachmentScanPending, TicketAttachmentScanMaxAttempts, now).
		Order("id ASC").Limit(10).Find(&rows).Error; err != nil {
		return 0, err
	}
	processed := 0
	var firstErr error
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		processed++
		if err := processTicketAttachmentScan(ctx, db, row, now); err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_attachment_scan_failed attachmentId=%d ticketId=%d error=%v", row.ID, row.TicketID, err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return processed, firstErr
}

func processTicketAttachmentScan(ctx context.Context, db *gorm.DB, row entity.TicketAttachment, scannedAt time.Time) error {
	// Claim：把 pending 推进到 scanning，条件包含读到的 attempt_count/updated_at，
	// 被别人抢先处理过的行这里会 RowsAffected=0，直接跳过。
	//
	// 时间**必须用调用方传入的 now**（而不是 time.Now()）：worker 的批量查询按 now
	// 判定 next_attempt_at，claim 若用更晚的真实时间，会让「刚刚被退避到未来」的行
	// 立刻可再次 claim，退避形同虚设（实测会导致 attempt_count 不增长、永远 pending）。
	claim := db.Model(&entity.TicketAttachment{}).
		Where("id = ? AND scan_state = ? AND attempt_count = ? AND updated_at = ?", row.ID, entity.TicketAttachmentScanPending, row.AttemptCount, row.UpdatedAt).
		Where("next_attempt_at IS NULL OR next_attempt_at <= ?", scannedAt).
		Updates(map[string]interface{}{"scan_state": entity.TicketAttachmentScanScanning, "updated_at": scannedAt})
	if claim.Error != nil {
		return claim.Error
	}
	if claim.RowsAffected == 0 {
		return nil
	}

	plain, mimeType, readErr := decryptTicketAttachmentForScan(db, row)
	var scan TicketAttachmentScanResult
	if readErr != nil {
		scan = TicketAttachmentScanResult{State: entity.TicketAttachmentScanFailed, CheckError: truncateUTF8(readErr.Error(), 500)}
	} else {
		scan = scanTicketAttachmentOnce(ctx, db, row.ID, plain, mimeType)
	}
	return persistTicketAttachmentScanResult(db, row, scan, scannedAt)
}

func decryptTicketAttachmentForScan(db *gorm.DB, row entity.TicketAttachment) ([]byte, string, error) {
	aead, err := ticketAttachmentCipher()
	if err != nil {
		return nil, "", err
	}
	if len(row.Nonce) != aead.NonceSize() {
		return nil, "", ErrTicketAttachmentInvalid
	}
	plain, err := aead.Open(nil, row.Nonce, row.Ciphertext,
		ticketAttachmentAAD(row.ID, row.UserID, row.MediaType))
	if err != nil {
		return nil, "", ErrTicketAttachmentInvalid
	}
	return plain, row.MediaType, nil
}

// persistTicketAttachmentScanResult 写回判定结果。
//
// failed 且还有重试额度 → 退回 pending 并按次数退避（1、2 分钟），
// 这样「上游临时 500」不会把图片永久钉成隐藏；额度用尽才定格在 failed。
//
// **优先级规则（上传前同步审核 vs 异步扫描）**：上传路径若已同步判违规，
// scan_state 在 INSERT 时就是 sensitive，异步 worker 的取数条件是
// scan_state = pending，因此根本不会选中它。这里的 UPDATE 再收一道口子
// （只在 scanning 状态上写）作为纵深防御：任何外部动作把行改成 sensitive 后，
// 异步结果都不能把它**改回 approved** —— 同步审核的结论更贴近上传时刻，
// 异步复核只允许「pending → approved/sensitive」，不允许放宽已有结论。
func persistTicketAttachmentScanResult(db *gorm.DB, row entity.TicketAttachment, scan TicketAttachmentScanResult, now time.Time) error {
	attempts := row.AttemptCount + 1
	updates := map[string]interface{}{
		"attempt_count": attempts,
		"nsfw_score":    scan.NSFWScore,
		"risk_level":    scan.RiskLevel,
		"suggestion":    scan.Suggestion,
		"label":         scan.Label,
		"check_error":   scan.CheckError,
		"checked_at":    now,
		"updated_at":    now,
	}
	switch scan.State {
	case entity.TicketAttachmentScanApproved, entity.TicketAttachmentScanSensitive:
		updates["scan_state"] = scan.State
		updates["next_attempt_at"] = nil
	default:
		if attempts < TicketAttachmentScanMaxAttempts {
			updates["scan_state"] = entity.TicketAttachmentScanPending
			next := now.Add(time.Duration(attempts) * time.Minute)
			updates["next_attempt_at"] = next
		} else {
			updates["scan_state"] = entity.TicketAttachmentScanFailed
			updates["next_attempt_at"] = nil
		}
	}
	// 只在「本次 claim 成功占住的 scanning 行」上写：若这期间状态已被改成
	// sensitive（上传前同步审核的结论），RowsAffected=0，异步结果被丢弃，
	// sensitive 得以保持。
	result := db.Model(&entity.TicketAttachment{}).
		Where("id = ? AND scan_state = ?", row.ID, entity.TicketAttachmentScanScanning).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 && scan.State == entity.TicketAttachmentScanApproved {
		// 正常情况（行仍是 scanning）不会走到这里；走到了说明状态在检测期间被
		// 改成非 scanning（典型：上传前已判 sensitive），此时不能静默——留一条
		// 可追溯的日志，说明为什么这张图没被改成 approved。
		logger.SysLog(fmt.Sprintf("ticket_attachment_scan_result_discarded attachmentId=%d want=%s reason=state_changed", row.ID, scan.State))
	}
	return nil
}

// RecoverStaleTicketAttachmentScans 把卡在 scanning 的行放回 pending。
// 与提醒 worker 一样只在启动与固定间隔调用，不在每次轮询里做。
func RecoverStaleTicketAttachmentScans(db *gorm.DB, now time.Time) (int64, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	staleBefore := now.Add(-2 * time.Minute)
	result := db.Model(&entity.TicketAttachment{}).
		Where("scan_state = ? AND updated_at < ?", entity.TicketAttachmentScanScanning, staleBefore).
		Updates(map[string]interface{}{"scan_state": entity.TicketAttachmentScanPending, "next_attempt_at": now, "updated_at": now})
	return result.RowsAffected, result.Error
}

// StartTicketAttachmentScanWorker 后台检测循环。返回的 channel 在循环退出后关闭，
// 与 StartTicketNotificationWorker 同构，便于 main 里统一等待。
func StartTicketAttachmentScanWorker(ctx context.Context, db *gorm.DB) <-chan struct{} {
	done := make(chan struct{})
	if db == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		RunTicketAttachmentScanWorker(ctx, db)
	}()
	return done
}

const (
	ticketAttachmentScanPollInterval     = 5 * time.Second
	ticketAttachmentScanRecoveryInterval = 5 * time.Minute
	// ticketAttachmentScanDisabledInterval 在没有 API key 时放慢轮询：
	// 每张图都会立刻得到 failed，重试 3 次后定格，没必要 5 秒一轮空转。
	ticketAttachmentScanDisabledInterval = 30 * time.Second
)

func RunTicketAttachmentScanWorker(ctx context.Context, db *gorm.DB) {
	if db == nil {
		return
	}
	db = db.WithContext(dbqueue.WithPriority(ctx, dbqueue.PriorityLow))
	nextRecovery := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		if nextRecovery.IsZero() || !now.Before(nextRecovery) {
			if _, err := RecoverStaleTicketAttachmentScans(db, now); err != nil {
				logger.SysWarn(fmt.Sprintf("ticket_attachment_scan_recovery_failed error=%v", err))
			}
			nextRecovery = now.Add(ticketAttachmentScanRecoveryInterval)
		}
		if _, err := ProcessPendingTicketAttachmentScans(ctx, db, now); err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_attachment_scan_poll_failed error=%v", err))
		}
		delay := ticketAttachmentScanPollInterval
		if strings.TrimSpace(GetUAPIAPIKeyWithDB(db)) == "" {
			delay = ticketAttachmentScanDisabledInterval
		}
		if !waitTicketAttachmentWorker(ctx, delay) {
			return
		}
	}
}

func waitTicketAttachmentWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
