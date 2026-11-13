package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 敏感图片检测的编排：缓存 → 额度 → pending 占位 → 外呼 → 写回。
//
// 完整判定链（111-*.md §1.2 的最终形态，含 117 号「检测跟 blob 走」的补充）：
//
//	① 计算 ImageSHA256
//	② 查 sensitive_image_cache_entries
//	     ├─ 命中 clean / sensitive → 直接返回结论（不外呼）；**尽力**扣 1 次额度，
//	     │                            扣不到（当日已满）就算了，仍然返回结论
//	     ├─ 命中 pending 且未超时   → 返回「检测中」，不重复外呼
//	     ├─ 命中 pending 但已超时   → 抢占，走 ③（重试不重复扣额度）
//	     └─ 未命中 / error          → 走 ③
//	③ 两道额度闸门（每工单 50 张 + 每人每日 20 张，见 sensitive_image_quota.go）
//	     └─ 超额 → 标记 sensitive_by_quota：图片照存、工单照提、**不外呼**
//	④ 写 pending 占位（唯一键 image_sha256 冲突 ⇒ 别人正在查，回到 ②）
//	⑤ 外呼检测 → 写回 clean / sensitive（失败写 error，留待下次抢占重试）
//
// 三条不可协商的性质：
//
//  1. **超额不拦工单**。额度是省钱闸，不是准入闸。图片照常落库、工单照常提交，
//     只把这张图标记成 sensitive_by_quota（默认按敏感处理 = 用户端隐藏）。
//     拦掉提交会让被误判的用户无法开单申辩。
//  2. **检测结果跟随 SHA256**。同一个 SHA256 在全世界只有一行缓存，
//     所以「同一张图在不同工单里只检测一次」是结构保证，不依赖业务判断。
//  3. **pending 占位是并发去重的唯一手段**。先插 pending 再做外呼：
//     插入成功 = 我是 leader；唯一键冲突 = 别人在查，等待或返回「检测中」。
//     没有这一列时，两个用户同时上传同一张新图会**各付一次**计费外呼。

// SensitiveImageDetectState 是一次敏感图检测的对外结论。
type SensitiveImageDetectState string

const (
	// SensitiveImageDetectClean 命中或检测完成，判定为正常。
	SensitiveImageDetectClean SensitiveImageDetectState = "clean"
	// SensitiveImageDetectSensitive 命中或检测完成，判定为敏感。
	SensitiveImageDetectSensitive SensitiveImageDetectState = "sensitive"
	// SensitiveImageDetectPending 同一张图正在被别的请求检测，本次未拿到结论。
	// 这不是错误：调用方应把图片按「检测中」处理（隐藏），稍后由管理员或
	// 用户重试拿到终态。
	SensitiveImageDetectPending SensitiveImageDetectState = "pending"
	// SensitiveImageDetectByQuota 额度耗尽且缓存未命中 —— 未检测、不外呼。
	// **图片照存、工单照提**，只是按敏感处理（可在管理页手动触发检测）。
	SensitiveImageDetectByQuota SensitiveImageDetectState = "sensitive_by_quota"
)

// sensitiveImagePendingTimeout 是 pending 占位的存活上限（109-*.md Q1-f 拍板 5 分钟）。
//
// 必须有超时：外呼进程崩溃 / 上游永久挂起时，占位会让这张图**永远**卡在
// 「检测中」，谁都拿不到结论。超时后允许抢占 —— 代价是可能重复一次外呼，
// 比永久不可用轻得多。
const sensitiveImagePendingTimeout = 5 * time.Minute

// sensitiveImageDetectProvider 是写进缓存的 provider 值。
const (
	SensitiveImageProviderUAPI   = "uapi"
	SensitiveImageProviderManual = "manual"
)

// SensitiveImageVerdict 是检测通道给出的结论（不含任何存储细节）。
type SensitiveImageVerdict struct {
	// Sensitive 为真表示判定为敏感。
	Sensitive bool
	// Reason 是可解释的一句话，会落进缓存与用户提示；**不得**放上游原始响应体。
	Reason string
	// Provider 记录判定来源，缺省（空串）按 uapi 记。
	Provider string
}

// SensitiveImageDetector 是**可注入**的检测通道。
//
// 为什么是函数类型而不是把网络调用写死：与 TicketImageModerator 同款理由 ——
// 「缓存命中零外呼」「额度耗尽零外呼」「并发只外呼一次」这三条都靠计数断言来钉，
// 对着真实上游做不出来。生产恒为 nil，此时走 uapi 的 /image/nsfw。
type SensitiveImageDetector func(ctx context.Context, data []byte, mimeType string) (SensitiveImageVerdict, error)

// sensitiveImageDetectorOverride 只给测试注入假通道用；生产恒为 nil。
var sensitiveImageDetectorOverride SensitiveImageDetector

// SensitiveImageDetectResult 是一次编排的完整结论。
type SensitiveImageDetectResult struct {
	ImageSHA256 string                    `json:"imageSha256"`
	State       SensitiveImageDetectState `json:"state"`
	// CacheHit 表示结论来自缓存（本次没有外呼）。
	CacheHit bool `json:"cacheHit"`
	// UpstreamCalled 表示本次真的发生了计费外呼。测试用它断言「零外呼」。
	UpstreamCalled bool `json:"upstreamCalled"`
	// Charged 表示本次消耗了 1 次每日额度。
	//
	// 三种取值的语义：
	//   - 缓存命中 + 有余量 → true（109-*.md「缓存命中也消耗额度」）
	//   - 缓存命中 + 已耗尽 → false（111 Q1-g「不计入」）
	//   - 未命中 + 超额     → false（根本没走到扣减）
	Charged    bool  `json:"charged"`
	UsedCount  int32 `json:"usedCount"`
	DailyLimit int   `json:"dailyLimit"`
	// PerTicketLimit 是每工单上限；TicketCount 是判定时刻该工单已有的图片数。
	//
	// 两个值都只用于**诊断与管理页展示**：超额时管理员要能回答「是工单满还是
	// 今日满」，而这两者的处置完全不同（前者要开新工单，后者只能等明天）。
	// 缓存命中路径不参与工单闸门，因此那里 TicketCount 保持 0。
	PerTicketLimit int   `json:"perTicketLimit"`
	TicketCount    int64 `json:"ticketCount"`
	// Reason 是给用户/管理员看的中文说明。
	Reason string `json:"reason,omitempty"`
	// TicketSubmitAllowed **恒为 true**：超额绝不阻断工单提交（107 Q1 ② 拍板）。
	// 显式保留这个字段是为了让调用方与测试都不必靠「State 不是 xxx」反推。
	TicketSubmitAllowed bool `json:"ticketSubmitAllowed"`
	// ScanState 是可直接写进 ticket_attachments.scan_state 的值。
	// 复用既有的 fail-closed 展示判据（只有 approved 才显示），不另造一套状态。
	ScanState string `json:"scanState"`
}

// HashSensitiveImage 返回图片二进制内容的 SHA256 十六进制串（缓存主键）。
//
// ⚠️ 必须对**重编码后的字节**调用（safeTicketAttachmentImage 的产物），
// 与 ticket_attachments.sha256 用同一份输入：否则「同一张图」在不同路径下
// 算出两个 hash，缓存去重就失效了。
func HashSensitiveImage(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DetectSensitiveImageWithDB 是敏感图检测的**唯一**编排入口。
//
// userID <= 0 或 ticketID < 0 视为调用方错误（返回 ErrTicketForbidden 同族错误不合适，
// 这里用明确的参数错误）：额度按用户算，没有用户就没有闸门。
//
// now 显式传入而不是内部 time.Now()：自然日键与 pending 超时都依赖它，
// 测试必须能确定性地控制「今天」和「占位是否过期」。
func DetectSensitiveImageWithDB(
	ctx context.Context,
	db *gorm.DB,
	userID int32,
	ticketID int64,
	data []byte,
	mimeType string,
	now time.Time,
) (SensitiveImageDetectResult, error) {
	if db == nil {
		return SensitiveImageDetectResult{}, errSensitiveImageQuotaNoDatabase
	}
	if userID <= 0 {
		return SensitiveImageDetectResult{}, errors.New("敏感图片检测缺少有效用户")
	}
	if len(data) == 0 {
		return SensitiveImageDetectResult{}, ErrTicketAttachmentInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}

	imageSHA := HashSensitiveImage(data)
	result := SensitiveImageDetectResult{
		ImageSHA256:         imageSHA,
		TicketSubmitAllowed: true,
	}
	policy := LoadSensitiveImageQuotaPolicyWithDB(db)
	result.DailyLimit = policy.DailyUserLimit
	result.PerTicketLimit = policy.PerTicketLimit
	date := SensitiveImageQuotaDate(now)

	entry, found, err := lookupSensitiveImageCacheWithDB(db, imageSHA)
	if err != nil {
		// 缓存读失败不阻断链路：降级成「没缓存」继续走额度 + 外呼。
		// 直接报错会让「缓存表一时不可用」变成「工单连图都传不了」。
		logger.SysLog(fmt.Sprintf("敏感图片缓存读取失败，按未命中处理：%v", err))
		found = false
	}

	if found {
		switch entry.Status {
		case entity.SensitiveImageCacheStatusClean, entity.SensitiveImageCacheStatusSensitive:
			return finishSensitiveImageCacheHit(db, entry, result)
		case entity.SensitiveImageCacheStatusPending:
			if sensitiveImagePendingIsStale(entry, now) {
				// 超时抢占：走外呼，但**不再扣额度** —— 这张图已经被扣过一次，
				// 重复扣会让「上游抖一下就吃掉用户额度」。
				return runSensitiveImageDetection(ctx, db, userID, ticketID, imageSHA, data, mimeType, policy, date, now, result, false)
			}
			result.State = SensitiveImageDetectPending
			result.ScanState = entity.TicketAttachmentScanScanning
			result.Reason = "该图片正在检测中，请稍后重试"
			return result, nil
		case entity.SensitiveImageCacheStatusError:
			// 上一次外呼失败：允许重试，同样不重复扣额度。
			return runSensitiveImageDetection(ctx, db, userID, ticketID, imageSHA, data, mimeType, policy, date, now, result, false)
		}
	}

	return runSensitiveImageDetection(ctx, db, userID, ticketID, imageSHA, data, mimeType, policy, date, now, result, true)
}

// finishSensitiveImageCacheHit 处理「缓存命中终态」分支。
//
// 这是**整个功能被修订最多次的一处**（109 → 110 → 111 三轮拍板），最终口径以
// 第五轮 111-*.md Q1-g 为准，**后轮覆盖前轮**（120 §4 修正链）：
//
//	缓存命中（clean / sensitive）→ 直接返回结论，**不查额度、不扣额度**
//
// 111 号四处一致证据：
//
//	§1.2 流程：「命中 → 【直接返回】，不扣额度、不查额度」；「⑤ 查当日额度」
//	        这一步的入口只有「未命中 → 走 ⑤」—— 命中根本走不到 ⑤。
//	例 2（已缓存 + 额度充足）：前置 used_count=3，例 1 后变 4，本例如是命中
//	        → 「used_count 仍是 4（不增加）」。
//	例 3（已缓存 + 额度已耗尽）：「直接返回 clean，【根本不去查额度】」。
//	§1.1 原话：「当额度已耗尽时，缓存命中直接返回结果 —— 不计入当天的额度消耗」。
//
// ⚠️ 被推翻的两版（不要照旧实现）：
//   - 109 §2.2（三轮）：「缓存命中也消耗额度」—— 已被 111 Q1-g 取代；
//   - 110 §1（四轮）：未耗尽扣、已耗尽不扣 —— 是中间的过渡口径，同被取代。
//
// 因此本函数**不调用 ConsumeSensitiveImageDailyQuotaWithDB**，也不触碰额度表的
// used_count：Charged 恒 false，UsedCount 只是把库里现值读出来给管理页看。
//
// 代价（111 §1.4 已明确接受）：已缓存的图可以无限次重复上传且不占额度 ——
// 额度限的是「触发新外呼的图」，不是「上传行为本身」。要防刷需另加「单日上传
// 总次数上限」这个独立计数器，当前不做。
//
// 工单闸门在这里同样**不参与**：它的作用是限制「一个工单能外呼多少张」，
// 而缓存命中根本不外呼，拿它去拦一张已经知道结论的图没有意义。
//
// 签名刻意只收 (db, entry, result)：不传 policy / userID / date 是**设计选择**——
// 只要参数在手，将来就会有人顺手加回「命中时扣一下」。参数里没有额度上下文，
// 这条规则就无法被误实现。
func finishSensitiveImageCacheHit(
	db *gorm.DB,
	entry entity.SensitiveImageCacheEntry,
	result SensitiveImageDetectResult,
) (SensitiveImageDetectResult, error) {
	result.CacheHit = true
	result.State = sensitiveImageStateOfCacheStatus(entry.Status)
	result.ScanState = sensitiveImageScanStateOfCacheStatus(entry.Status)
	result.Reason = entry.Reason
	// Charged 保持零值 false：命中不消耗额度（111 Q1-g）。
	result.Charged = false

	// 命中计数只增，失败不影响结论（它纯粹是统计，与额度无关）。
	if err := db.Model(&entity.SensitiveImageCacheEntry{}).
		Where(map[string]any{"image_sha256": entry.ImageSHA256}).
		UpdateColumn("hit_count", gorm.Expr("hit_count + 1")).Error; err != nil {
		logger.SysLog(fmt.Sprintf("敏感图片缓存命中计数写入失败（不影响结论）：%v", err))
	}
	// 本函数到此为止：**不读额度表、不写额度表**。
	//
	// 111 §1.2 的原话是「不扣额度、不查额度」—— 「不查」这两个字必须照做：
	// 命中路径既然不去决策，就连读一眼的必要都没有。管理页要看当日用量请走
	// SensitiveImageQuotaStatus（独立接口，专为展示设计），不要让它搭 detect 的便车。
	return result, nil
}

// runSensitiveImageDetection 走额度 + pending 占位 + 外呼。
//
// charge 为 true 时才扣额度：首次拿这张新图要扣，抢占超时占位 / 重试 error 行不扣
// （同一张图不重复计费）。
//
// ticketID 只参与**工单闸门**（每工单 50 张），且只在 charge=true 时生效：
// 抢占/重试的是已经计过数的同一张图，再拿工单上限去拦它等于让超时占位永远拿不到结论。
func runSensitiveImageDetection(
	ctx context.Context,
	db *gorm.DB,
	userID int32,
	ticketID int64,
	imageSHA string,
	data []byte,
	mimeType string,
	policy SensitiveImageQuotaPolicy,
	date string,
	now time.Time,
	result SensitiveImageDetectResult,
	charge bool,
) (SensitiveImageDetectResult, error) {
	gateTicketID := int64(0)
	if charge {
		gateTicketID = ticketID
	}
	reservation, err := ReserveSensitiveImageQuotaWithDB(db, policy, userID, gateTicketID, date, charge)
	if err != nil {
		return result, err
	}
	result.UsedCount = reservation.UsedCount
	result.TicketCount = reservation.TicketCount
	if charge && !reservation.Allowed {
		// 额度耗尽 + 缓存未命中：标 sensitive_by_quota，**不外呼**。
		// 图片照存、工单照提（TicketSubmitAllowed 恒 true）。
		result.State = SensitiveImageDetectByQuota
		result.ScanState = entity.TicketAttachmentScanSensitive
		result.Reason = reservation.Reason
		return result, nil
	}
	result.Charged = reservation.Charged

	leader, err := claimSensitiveImagePendingWithDB(db, imageSHA, mimeType, len(data), now)
	if err != nil {
		return result, err
	}
	if !leader {
		// 唯一键冲突：别人正在查同一张图。再读一次，可能已经拿到终态。
		entry, found, readErr := lookupSensitiveImageCacheWithDB(db, imageSHA)
		if readErr == nil && found {
			switch entry.Status {
			case entity.SensitiveImageCacheStatusClean, entity.SensitiveImageCacheStatusSensitive:
				result.CacheHit = true
				result.State = sensitiveImageStateOfCacheStatus(entry.Status)
				result.ScanState = sensitiveImageScanStateOfCacheStatus(entry.Status)
				result.Reason = entry.Reason
				return result, nil
			}
		}
		result.State = SensitiveImageDetectPending
		result.ScanState = entity.TicketAttachmentScanScanning
		result.Reason = "该图片正在检测中，请稍后重试"
		return result, nil
	}

	detector := sensitiveImageDetectorWithDB(db)
	if detector == nil {
		// 没配置检测通道：不留 pending（否则这张图会卡到超时才被放行），
		// 直接把缓存标成 error 并如实返回「未配置」。与 scanTicketAttachmentOnce
		// 「没 key 不假装成功」同一取向。
		_ = markSensitiveImageCacheErrorWithDB(db, imageSHA, "未配置图片检测通道", now)
		result.State = SensitiveImageDetectPending
		result.ScanState = entity.TicketAttachmentScanFailed
		result.Reason = "未配置敏感图片检测通道"
		return result, nil
	}

	result.UpstreamCalled = true
	verdict, callErr := detector(ctx, data, mimeType)
	if callErr != nil {
		// 失败写 error（不是终态）：下次上传同一张图会抢占重试，不会再扣额度。
		_ = markSensitiveImageCacheErrorWithDB(db, imageSHA, callErr.Error(), now)
		result.State = SensitiveImageDetectPending
		result.ScanState = entity.TicketAttachmentScanFailed
		result.Reason = "敏感图片检测暂时不可用"
		logger.SysWarn(fmt.Sprintf("sensitive_image_detect_failed sha256=%s error=%v", imageSHA, callErr))
		return result, nil
	}

	provider := strings.TrimSpace(verdict.Provider)
	if provider == "" {
		provider = SensitiveImageProviderUAPI
	}
	status := entity.SensitiveImageCacheStatusClean
	if verdict.Sensitive {
		status = entity.SensitiveImageCacheStatusSensitive
	}
	if err := markSensitiveImageCacheResultWithDB(db, imageSHA, status, truncateUTF8(verdict.Reason, 255), provider, now); err != nil {
		// 写缓存失败不影响本次结论：用户这次拿到的判定是真的，只是下次还要再算一遍。
		logger.SysLog(fmt.Sprintf("敏感图片检测结果写入缓存失败（结论照常返回）：%v", err))
	}
	result.State = sensitiveImageStateOfCacheStatus(status)
	result.ScanState = sensitiveImageScanStateOfCacheStatus(status)
	result.Reason = verdict.Reason
	return result, nil
}

// lookupSensitiveImageCacheWithDB 按主键读一行缓存。
func lookupSensitiveImageCacheWithDB(db *gorm.DB, imageSHA string) (entity.SensitiveImageCacheEntry, bool, error) {
	var entry entity.SensitiveImageCacheEntry
	err := db.Where(map[string]any{"image_sha256": imageSHA}).First(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.SensitiveImageCacheEntry{}, false, nil
	}
	if err != nil {
		return entity.SensitiveImageCacheEntry{}, false, err
	}
	return entry, true, nil
}

// claimSensitiveImagePendingWithDB 抢占某张图的**检测权**。
//
// 返回 leader=true 表示本次请求拿到了检测权，只有 leader 允许外呼。
//
// 两条路径，缺一不可：
//
//  1. **插入占位**（新图）：靠主键唯一约束判胜负 —— OnConflict DoNothing 让后到者的
//     INSERT 变成空操作，RowsAffected=0 即「别人已经在查了」。这比「先 SELECT 再
//     INSERT」可靠：后者在并发下有窗口，两个请求都会读到「不存在」然后各调一次上游。
//
//  2. **抢占旧行**（重试 / 超时）：行已经存在时插入必然被忽略，所以必须显式 UPDATE。
//     可抢占的两种状态都只有同一份来源：**上一次没能给出结论**。
//     - error：上次外呼失败，非终态，允许立刻重试；
//     - pending 且 updated_at 超过存活上限：上游挂起/进程崩溃留下的占位。
//     条件 UPDATE 让并发抢占只有一个胜者（RowsAffected=1），落败方照样走
//     「检测中」分支，因此不会重复外呼。
//
// 为什么不用「先删再插」：删掉占位会让并发的第三方看到「无行」也去插入，
// 于是又回到重复外呼。UPDATE 是原子的、不产生空洞。
func claimSensitiveImagePendingWithDB(db *gorm.DB, imageSHA, mimeType string, size int, now time.Time) (bool, error) {
	placeholder := entity.SensitiveImageCacheEntry{
		ImageSHA256: imageSHA,
		ContentType: strings.TrimSpace(mimeType),
		FileSize:    int64(size),
		Status:      entity.SensitiveImageCacheStatusPending,
		Provider:    SensitiveImageProviderUAPI,
		UpdatedAt:   now,
	}
	insert := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&placeholder)
	if insert.Error != nil {
		return false, insert.Error
	}
	if insert.RowsAffected > 0 {
		return true, nil
	}
	// 行已存在：尝试从「上一次没给出结论」的状态抢占回来。
	staleBefore := now.Add(-sensitiveImagePendingTimeout)
	takeover := db.Model(&entity.SensitiveImageCacheEntry{}).
		Where(map[string]any{"image_sha256": imageSHA}).
		Where(
			db.Where("status = ?", entity.SensitiveImageCacheStatusError).
				Or("status = ? AND updated_at <= ?", entity.SensitiveImageCacheStatusPending, staleBefore),
		).
		Updates(map[string]any{
			"status":       entity.SensitiveImageCacheStatusPending,
			"reason":       "",
			"content_type": strings.TrimSpace(mimeType),
			"file_size":    int64(size),
			"updated_at":   now,
		})
	if takeover.Error != nil {
		return false, takeover.Error
	}
	return takeover.RowsAffected > 0, nil
}

// markSensitiveImageCacheResultWithDB 把 pending 占位改成终态。
//
// 条件里带 status='pending'：如果管理员在此期间手动触发了检测并已写入结论，
// 本次结果不得把它**改回**另一个结论（与 ticket_attachment 的
// 「异步复核不允许放宽已有结论」同一取向）。
// 但 error 也算可覆盖 —— 它本来就不是结论。
func markSensitiveImageCacheResultWithDB(db *gorm.DB, imageSHA, status, reason, provider string, now time.Time) error {
	return db.Model(&entity.SensitiveImageCacheEntry{}).
		Where(map[string]any{"image_sha256": imageSHA}).
		Where("status IN ?", []string{entity.SensitiveImageCacheStatusPending, entity.SensitiveImageCacheStatusError}).
		Updates(map[string]any{
			"status":     status,
			"reason":     reason,
			"provider":   provider,
			"updated_at": now,
		}).Error
}

// markSensitiveImageCacheErrorWithDB 把占位标成 error（非终态，可被下次抢占重试）。
func markSensitiveImageCacheErrorWithDB(db *gorm.DB, imageSHA, reason string, now time.Time) error {
	return db.Model(&entity.SensitiveImageCacheEntry{}).
		Where(map[string]any{"image_sha256": imageSHA}).
		Where("status = ?", entity.SensitiveImageCacheStatusPending).
		Updates(map[string]any{
			"status":     entity.SensitiveImageCacheStatusError,
			"reason":     truncateUTF8(reason, 255),
			"updated_at": now,
		}).Error
}

// sensitiveImagePendingIsStale 判断占位是否已超过存活上限（可被抢占）。
func sensitiveImagePendingIsStale(entry entity.SensitiveImageCacheEntry, now time.Time) bool {
	if entry.UpdatedAt.IsZero() {
		return true
	}
	return now.Sub(entry.UpdatedAt) >= sensitiveImagePendingTimeout
}

// sensitiveImageStateOfCacheStatus 把缓存状态映射成对外结论。
func sensitiveImageStateOfCacheStatus(status string) SensitiveImageDetectState {
	if strings.EqualFold(strings.TrimSpace(status), entity.SensitiveImageCacheStatusClean) {
		return SensitiveImageDetectClean
	}
	return SensitiveImageDetectSensitive
}

// sensitiveImageScanStateOfCacheStatus 把缓存状态映射成 ticket_attachments.scan_state。
//
// 复用既有状态机而不是新造一套：展示层只认 approved（fail-closed），
// 因此 clean→approved、sensitive→sensitive，其余（pending/error）保持隐藏。
func sensitiveImageScanStateOfCacheStatus(status string) string {
	if strings.EqualFold(strings.TrimSpace(status), entity.SensitiveImageCacheStatusClean) {
		return entity.TicketAttachmentScanApproved
	}
	return entity.TicketAttachmentScanSensitive
}

// sensitiveImageDetectorWithDB 返回本次要用的检测通道。
//
// 优先级：测试注入 > 生产 uapi 实现。返回 nil 表示「没配置，不可用」。
func sensitiveImageDetectorWithDB(db *gorm.DB) SensitiveImageDetector {
	if sensitiveImageDetectorOverride != nil {
		return sensitiveImageDetectorOverride
	}
	key := strings.TrimSpace(GetUAPIAPIKeyWithDB(db))
	if !strings.HasPrefix(key, "uapi-") || len(key) <= len("uapi-") {
		return nil
	}
	return func(ctx context.Context, data []byte, mimeType string) (SensitiveImageVerdict, error) {
		scan := scanTicketAttachmentOnce(ctx, db, 0, data, mimeType)
		switch scan.State {
		case entity.TicketAttachmentScanApproved:
			return SensitiveImageVerdict{Sensitive: false, Provider: SensitiveImageProviderUAPI}, nil
		case entity.TicketAttachmentScanSensitive:
			reason := strings.TrimSpace(scan.RiskLevel)
			if reason == "" {
				reason = "上游判定为敏感图片"
			} else {
				reason = "上游判定为敏感图片（风险等级 " + reason + "）"
			}
			return SensitiveImageVerdict{Sensitive: true, Reason: reason, Provider: SensitiveImageProviderUAPI}, nil
		default:
			// failed：上游 200 但没给出可用结论 —— 按「没跑成」处理，
			// 绝不当成 clean（那等于把审核绕过变成一次上游抖动）。
			return SensitiveImageVerdict{}, fmt.Errorf("敏感图片检测结论不可用: %s", truncateUTF8(scan.CheckError, 200))
		}
	}
}

// ManualDetectSensitiveImageWithDB 是管理员「手动触发检测」的入口（107 Q1 ③ 按工单批量）。
//
// 与用户路径的三处差异：
//  1. **完全绕过额度闸门**。管理员触发是运营复核，不是用户上传，
//     不该因为某个用户当天额度用满就查不了。
//  2. **允许覆盖已有结论**（provider 记为 manual）：这正是「模型更新后重判」的用途。
//  3. 仍然只外呼一次并把结论写回同一个 SHA256 行 —— 手动复核的结论对所有
//     引用这张图的工单同时生效，这也是「检测跟 SHA256 走」的直接后果。
func ManualDetectSensitiveImageWithDB(
	ctx context.Context,
	db *gorm.DB,
	data []byte,
	mimeType string,
	now time.Time,
) (SensitiveImageDetectResult, error) {
	if db == nil {
		return SensitiveImageDetectResult{}, errSensitiveImageQuotaNoDatabase
	}
	if len(data) == 0 {
		return SensitiveImageDetectResult{}, ErrTicketAttachmentInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	imageSHA := HashSensitiveImage(data)
	result := SensitiveImageDetectResult{ImageSHA256: imageSHA, TicketSubmitAllowed: true}

	detector := sensitiveImageDetectorWithDB(db)
	if detector == nil {
		return result, errors.New("未配置敏感图片检测通道")
	}
	result.UpstreamCalled = true
	verdict, err := detector(ctx, data, mimeType)
	if err != nil {
		// 手动触发失败要把错误如实返回给管理员（与用户路径的静默降级相反）。
		_ = markSensitiveImageCacheErrorWithDB(db, imageSHA, err.Error(), now)
		return result, fmt.Errorf("敏感图片检测失败：%w", err)
	}
	status := entity.SensitiveImageCacheStatusClean
	if verdict.Sensitive {
		status = entity.SensitiveImageCacheStatusSensitive
	}
	// 手动触发必须能覆盖已有结论，因此这里不用「只改 pending/error」的条件更新。
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "image_sha256"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "reason", "provider", "content_type", "file_size", "updated_at"}),
	}).Create(&entity.SensitiveImageCacheEntry{
		ImageSHA256: imageSHA,
		ContentType: strings.TrimSpace(mimeType),
		FileSize:    int64(len(data)),
		Status:      status,
		Reason:      truncateUTF8(verdict.Reason, 255),
		Provider:    SensitiveImageProviderManual,
		UpdatedAt:   now,
	}).Error; err != nil {
		return result, fmt.Errorf("写入敏感图片检测结果失败：%w", err)
	}
	result.State = sensitiveImageStateOfCacheStatus(status)
	result.ScanState = sensitiveImageScanStateOfCacheStatus(status)
	result.Reason = verdict.Reason
	return result, nil
}

// ---- 管理页支撑：手动触发与状态同步 ----

var (
	// ErrSensitiveImageBytesUnavailable 表示缓存里有结论，但找不到对应的原始图片字节
	// （例如附件已被删除）。手动重检必须拿字节去外呼，没有字节就只能如实报错，
	// 不能假装重新检测成功。
	ErrSensitiveImageBytesUnavailable = errors.New("找不到该图片的原始字节")
)

// SensitiveImageQuotaNow 是编排层取「现在」的唯一入口。
//
// 抽出来的目的：自然日键（Asia/Shanghai）与 pending 超时都依赖它。
// 单测里替换它就能确定性地控制「今天」与「占位是否过期」，
// 不必让每个调用点都传 now（controller 没有 time 参数可传）。
var SensitiveImageQuotaNow = func() time.Time { return time.Now() }

// SensitiveImageQuotaStatusDefaultDate 返回「今天」的自然日键（管理页缺省值）。
func SensitiveImageQuotaStatusDefaultDate() string {
	return SensitiveImageQuotaDate(SensitiveImageQuotaNow())
}

// LoadSensitiveImageBytesWithDB 按 SHA256 找到一张附件的明文与媒体类型。
//
// 为什么需要它：缓存表**刻意不存图片字节**（只存判定结论），所以「管理员按
// SHA256 重判一张图」必须回到 ticket_attachments 里取字节并解密。
// 同一 SHA256 可能有多行（同一张图被不同工单上传），任意一行都够用 ——
// 内容相同，hash 已经证明了这一点。
func LoadSensitiveImageBytesWithDB(db *gorm.DB, imageSHA256 string) ([]byte, string, error) {
	if db == nil {
		return nil, "", errSensitiveImageQuotaNoDatabase
	}
	imageSHA256 = strings.ToLower(strings.TrimSpace(imageSHA256))
	if imageSHA256 == "" {
		return nil, "", ErrSensitiveImageBytesUnavailable
	}
	var row entity.TicketAttachment
	err := db.Where(map[string]any{"sha256": imageSHA256}).Order("id ASC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", ErrSensitiveImageBytesUnavailable
	}
	if err != nil {
		return nil, "", err
	}
	plain, mimeType, err := decryptTicketAttachmentForScan(db, row)
	if err != nil {
		// 解密失败（密钥换了 / 行残缺）同样归入「拿不到字节」：
		// 对调用方而言这两种情况的处置完全一样（无法重检）。
		return nil, "", ErrSensitiveImageBytesUnavailable
	}
	return plain, mimeType, nil
}

// SyncSensitiveImageScanStateWithDB 把某张图的结论同步到所有引用它的附件行。
//
// 这是「检测跟 SHA256 走」的必然推论：缓存里只有一行结论，但附件可能有多行
// （同一张图被不同工单、不同用户上传）。只改缓存不改附件，表现就是
// 「管理页说已重判为 clean，用户看到的图还是隐藏」—— 这类不一致极难排查。
//
// 只改 scan_state / checked_at，不动 attempt_count / next_attempt_at：
// 手动重判不是异步重试，改动重试计数会让后台 worker 的行为凭空变化。
func SyncSensitiveImageScanStateWithDB(db *gorm.DB, imageSHA256, state string) (int64, error) {
	if db == nil {
		return 0, errSensitiveImageQuotaNoDatabase
	}
	imageSHA256 = strings.ToLower(strings.TrimSpace(imageSHA256))
	if imageSHA256 == "" {
		return 0, ErrSensitiveImageBytesUnavailable
	}
	scanState := entity.TicketAttachmentScanSensitive
	if strings.EqualFold(strings.TrimSpace(state), string(SensitiveImageDetectClean)) {
		scanState = entity.TicketAttachmentScanApproved
	}
	now := SensitiveImageQuotaNow()
	update := db.Model(&entity.TicketAttachment{}).
		Where(map[string]any{"sha256": imageSHA256}).
		Updates(map[string]any{"scan_state": scanState, "checked_at": now, "updated_at": now})
	if update.Error != nil {
		return 0, update.Error
	}
	return update.RowsAffected, nil
}

// SensitiveImageTicketDetectResult 是按工单批量触发的结果（107 Q1 ③）。
type SensitiveImageTicketDetectResult struct {
	TicketID int64 `json:"ticketId"`
	// Total 是该工单的附件总数；Detected 是本次真的重新检测的张数；
	// Skipped 是字节取不到（行残缺/密钥变更）而被跳过的张数。
	Total    int                          `json:"total"`
	Detected int                          `json:"detected"`
	Skipped  int                          `json:"skipped"`
	Results  []SensitiveImageDetectResult `json:"results"`
}

// ManualDetectSensitiveImageTicketWithDB 对一个工单的所有图片批量重新检测（管理员）。
//
// 为什么按工单而不是按「超额记录」：超额时**根本没有写缓存**（额度耗尽就 return 了），
// 所以「这张工单有哪些图被额度拦下」只能从 ticket_attachments 反查。
// 按工单全量重检还有个好处：顺手把之前 error / pending 的图一起补上。
//
// 去重：同一张图在本工单里出现多次时只外呼一次（按 SHA256 去重），
// 否则一个工单贴 5 张相同的截图会白付 5 次调用费。
func ManualDetectSensitiveImageTicketWithDB(ctx context.Context, db *gorm.DB, ticketID int64) (SensitiveImageTicketDetectResult, error) {
	result := SensitiveImageTicketDetectResult{TicketID: ticketID}
	if db == nil {
		return result, errSensitiveImageQuotaNoDatabase
	}
	if ticketID <= 0 {
		return result, errors.New("需要提供有效的工单编号")
	}
	var rows []entity.TicketAttachment
	if err := db.Where(map[string]any{"ticket_id": ticketID}).Order("id ASC").Find(&rows).Error; err != nil {
		return result, err
	}
	result.Total = len(rows)
	if len(rows) == 0 {
		return result, nil
	}
	now := SensitiveImageQuotaNow()
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		sha := strings.ToLower(strings.TrimSpace(row.SHA256))
		if sha != "" {
			if _, done := seen[sha]; done {
				// 同一张图已在本次批量里处理过：不重复外呼，也不再算一次。
				continue
			}
			seen[sha] = struct{}{}
		}
		plain, mimeType, err := decryptTicketAttachmentForScan(db, row)
		if err != nil {
			result.Skipped++
			continue
		}
		detected, err := ManualDetectSensitiveImageWithDB(ctx, db, plain, mimeType, now)
		if err != nil {
			result.Skipped++
			continue
		}
		result.Detected++
		result.Results = append(result.Results, detected)
		if sha != "" {
			if _, syncErr := SyncSensitiveImageScanStateWithDB(db, sha, string(detected.State)); syncErr != nil {
				logger.SysWarn(fmt.Sprintf("敏感图片复判后同步附件状态失败 sha256=%s error=%v", sha, syncErr))
			}
		}
	}
	return result, nil
}
