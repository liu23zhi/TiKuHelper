package service

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 退款工单与通用工单的合并（T-209，用户原话：
// 「工单反馈 请合并 退款工单。退款工单 也显示在工单列表。
//   退款工单需要直接合并到工单里面。新建工单的时候让用户选择工单类型」）。
//
// # 合并方式：壳工单 + 单向关联（69-*.md §8.4 的裁决）
//
// 文档 69 §2.2 把 `refund` 列为工单分类之一，但同文 §8.4 明确建议
// 「`refund` 类别**不并入**本系统状态机，`ticket_refund` 已有独立流程，只做关联」。
// 实测支持这个裁决：
//
//   - 工单是 **4 态**（open/pending/resolved/closed）；
//   - 退款是 **7 态**（pending_review/rejected/frozen/processing/unknown/succeeded/failed），
//     而且 frozen / processing / unknown 三个状态的唯一含义都是「**资金还冻着**」，
//     它们决定管理员该不该重试打款；
//   - 退款每次状态迁移都在**同一事务**里写钱包 hold 与 ledger（见
//     ticket_refund.go 的 Review/Begin/Reconcile 路径）。
//
// 把退款状态塞进 tickets.status 会有两个不可接受的后果：① 7 态映射到 4 态必然丢信息，
// 「冻结中」与「处理中」都只能落进 pending，管理员再也看不出钱在哪；② 退款状态写入点
// 分散在 8 处且都带账务，让它们同时维护 tickets.status 就是在账务路径上多加一个失败点。
//
// 因此合并做在**列表与入口**这一层，而不是状态列：
//   - 每笔退款在同一事务里建一张 `tickets` 壳工单（category=refund，origin=user）；
//   - 壳工单的 status 由退款状态**映射**得出（展示口径，见 ticketRefundShellStatus）；
//   - 真正的操作仍走 `/ticket-refunds`，通过 ticket_refunds.ticket_id 定位。
//
// # 回滚
//
// 本次合并**不删改任何退款业务字段**（状态机、金额、ledger 一律不动），
// 新增的只有 ticket_refunds.ticket_id 列与 tickets 里的壳工单行。回滚 =
// 停用壳工单 + `ALTER TABLE ticket_refunds DROP COLUMN ticket_id`（迁移标记
// `ticket_refund_ticket_id_schema_ready` 一并清掉即可重放）。数据可逆。

// ErrTicketRefundTicketLink 表示退款与壳工单的关联状态不允许该操作。
var ErrTicketRefundTicketLink = errors.New("退款工单关联状态不允许该操作")

// ticketRefundShellSubject 是壳工单的标题（列表里要能一眼看出是退款）。
func ticketRefundShellSubject(refund entity.TicketRefund) string {
	if orderNo := strings.TrimSpace(refund.OrderNo); orderNo != "" {
		return truncateRunes(fmt.Sprintf("退款申请 · 订单 %s", orderNo), TicketSubjectMaxLength)
	}
	return truncateRunes(fmt.Sprintf("退款申请 · #%d", refund.ID), TicketSubjectMaxLength)
}

// truncateRunes 按 rune 截断，避免把多字节字符切坏。
func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// ticketRefundShellBody 是壳工单的首条消息正文。
//
// 刻意写清「这里不是操作入口」：壳工单存在的意义是让退款出现在列表里，
// 真正的审核/打款/对账必须在退款详情做（金额与钱包 hold 都在那边）。
func ticketRefundShellBody(refund entity.TicketRefund) string {
	var lines []string
	lines = append(lines, "本工单由退款申请自动创建，用于在工单列表中展示退款进度。")
	lines = append(lines, fmt.Sprintf("退款单号：#%d", refund.ID))
	if orderNo := strings.TrimSpace(refund.OrderNo); orderNo != "" {
		lines = append(lines, "关联订单："+orderNo)
	}
	if reason := strings.TrimSpace(refund.Reason); reason != "" {
		lines = append(lines, "申请理由："+reason)
	}
	lines = append(lines, "审核、打款与对账请在「退款」详情中操作，不要在本工单里回复金额。")
	return strings.Join(lines, "\n")
}

// ticketRefundShellStatus 把退款 7 态映射成工单 4 态（**展示口径**）。
//
// 映射规则与理由：
//   - 待审核 / 已通过冻结 → open：都还在等管理员推进；
//   - 处理中 / 结果待核对 → pending：都在等**外部渠道**回话，等价于「等回复」；
//   - 已驳回 / 成功 / 未完成 → closed：都是终态，管理员不需要再动；
//     注意 failed 也是终态（冻结已解除），留在 open 会让它永远挂在待办里。
//
// ⚠️ 这个映射**只影响壳工单的展示状态**，绝不反向写回退款状态。
func ticketRefundShellStatus(refundStatus string) string {
	switch strings.ToLower(strings.TrimSpace(refundStatus)) {
	case entity.TicketRefundStatusPendingReview, entity.TicketRefundStatusFrozen:
		return entity.TicketStatusOpen
	case entity.TicketRefundStatusProcessing, entity.TicketRefundStatusUnknown:
		return entity.TicketStatusPending
	case entity.TicketRefundStatusRejected, entity.TicketRefundStatusSucceeded,
		entity.TicketRefundStatusFailed:
		return entity.TicketStatusClosed
	default:
		// 未知状态按「还需管理员看一眼」处理，不要静默归档。
		return entity.TicketStatusOpen
	}
}

// ticketRefundShellPriority 是壳工单的优先级。
//
// 固定 normal：69-*.md §2.3 对 refund 的建议就是「常规业务」
// （涉及钱但不是「完全无法使用产品」，不像封禁申诉那样要最短等待）。
// 真正紧急的信号是退款状态本身（frozen / processing），由列表列展示。
func ticketRefundShellPriority() string { return entity.TicketPriorityNormal }

// CreateTicketRefundShellInTx 在一笔退款的**同一事务**里建壳工单并写回 ticket_id。
//
// 导出是为了让 internal/registry 的存量回填也能用同一份实现（回填必须在
// 「插工单 → 写回关联」的同一事务里完成，否则重复回填会再造一张单）。
// 业务写入路径见 CreateTicketRefundWithDB（它在本事务内调用本函数）。
//
// 放在同一事务的理由：退款落库成功但壳工单没建 = 该退款在工单列表里永久消失；
// 壳工单建了但 ticket_id 没写回 = 重复回填会再造一张单（列表里出现两条一样的退款）。
// 两者都必须在同一个提交里成立或一起回滚。
//
// 幂等：如果该退款已经有 ticket_id，直接返回，不重复建单。
// 这让「创建退款」与「存量回填」可以安全地共用本函数。
//
// now 为零值时取当前时间（回填路径传零值，让壳工单用当前时间而不是伪造历史时间）。
func CreateTicketRefundShellInTx(tx *gorm.DB, refund entity.TicketRefund, now time.Time) error {
	if tx == nil {
		return ErrTicketRefundInvalid
	}
	if refund.ID <= 0 || refund.UserID <= 0 {
		return ErrTicketRefundTicketLink
	}
	// ⚠️ 工单表不存在时**跳过壳工单**而不是让退款失败。
	//
	// 这条判据来自实测：加壳工单后，既有的退款测试库（只迁 ticket_refunds +
	// recharge_orders，不含 tickets）在 create 这一步直接报 `no such table: tickets`，
	// 连带 7 条无关的退款金额/资格用例全红。
	//
	// 这里必须权衡两件事的优先级：**退款能否落库** 与 **退款是否出现在工单列表**。
	// 前者是钱的动作，后者是展示；展示层的依赖缺失绝不能反过来否决退款本身。
	// 因此缺表时退款照常创建（ticket_id 留空），后续由迁移回填补齐 —— 这与
	// 「迁移没跑完」的真实场景一致（那时 tickets 表可能确实还没建）。
	if !tx.Migrator().HasTable(entity.TableNameTicket) || !tx.Migrator().HasTable(entity.TableNameTicketMessage) {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	// 已关联：幂等返回。读一次确认工单真的存在（关联列可能指向被删的工单）。
	if refund.TicketID != nil && *refund.TicketID > 0 {
		var existing entity.Ticket
		err := tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", *refund.TicketID).Take(&existing).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// 关联悬空：下面重建一张并覆盖关联。
	}

	shell := entity.Ticket{
		UserID:        refund.UserID,
		UserName:      lookupTicketUserName(tx, refund.UserID),
		Subject:       ticketRefundShellSubject(refund),
		Status:        ticketRefundShellStatus(refund.Status),
		Priority:      ticketRefundShellPriority(),
		Origin:        entity.TicketOriginUser,
		Category:      entity.TicketCategoryRefund,
		LastMessageAt: now,
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&shell).Error; err != nil {
		return err
	}

	message := entity.TicketMessage{
		TicketID: shell.ID, UserID: refund.UserID,
		AuthorName: "系统自动创建", Body: ticketRefundShellBody(refund),
		AuthorRole: entity.TicketAuthorRoleSystem, CreatedAt: now,
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&message).Error; err != nil {
		return err
	}

	// 把新工单 ID 写回退款行。条件里带上 `ticket_id IS NULL`：
	// 并发创建（用户连点两次申请、或回填与创建同时跑）时只有一方能写成功。
	result := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{}).
		Where("id = ? AND ticket_id IS NULL", refund.ID).
		UpdateColumn("ticket_id", shell.ID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// 另一路已经写上了关联：本事务里新建的壳工单是孤儿，回滚掉。
		return ErrTicketRefundTicketLink
	}
	return nil
}

// syncTicketRefundShellStatusInTx 把退款的最新状态映射到壳工单（只动 tickets.status）。
//
// 调用点：退款状态每次迁移之后（审核 / 开始打款 / 对账）。
//
// 为什么**不**在退款状态迁移的事务里做：退款路径已经在同一事务里写钱包与 ledger，
// 再往里加一次 tickets 更新会把账务事务的失败面扩大（壳工单写失败会让打款回滚，
// 那是最不该发生的事）。所以壳工单状态用**尽力而为**的独立更新：
// 失败只记日志，退款本身照常推进；下一次状态迁移或回填会把它修正回来。
//
// 这也意味着壳工单状态是**最终一致**的展示口径，不是账务事实 —— 与它本来的定位一致。
func syncTicketRefundShellStatusInTx(db *gorm.DB, refund entity.TicketRefund) {
	if db == nil || refund.TicketID == nil || *refund.TicketID <= 0 {
		return
	}
	status := ticketRefundShellStatus(refund.Status)
	closedAt := any(nil)
	if status == entity.TicketStatusClosed {
		closedAt = time.Now()
	}
	updates := map[string]any{"status": status, "updated_at": time.Now()}
	if closedAt != nil {
		updates["closed_at"] = closedAt
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.Ticket{}).
		Where("id = ?", *refund.TicketID).
		Updates(updates).Error; err != nil {
		logger.SysLog(fmt.Sprintf("同步退款壳工单状态失败（不影响退款本身，下次状态迁移会修正）：%v", err))
	}
}

// GetTicketRefundByTicketIDWithDB 按壳工单 ID 反查退款。
//
// 列表里点开一张 category=refund 的工单时，前端需要知道去哪个退款详情操作。
// 返回 found=false 表示「这张退款工单还没有关联」（迁移未跑完或历史脏数据），
// 调用方应展示「退款记录暂不可用」而不是报错。
func GetTicketRefundByTicketIDWithDB(db *gorm.DB, actor entity.User, ticketID int64) (entity.TicketRefund, bool, error) {
	if db == nil || ticketID <= 0 {
		return entity.TicketRefund{}, false, ErrTicketRefundInvalid
	}
	fresh, err := ticketRefundActor(db, actor.ID, false, false)
	if err != nil {
		return entity.TicketRefund{}, false, err
	}
	query := db.Session(&gorm.Session{NewDB: true}).Where("ticket_id = ?", ticketID)
	if !isAdminUser(fresh) {
		query = query.Where("user_id = ?", fresh.ID)
	}
	var row entity.TicketRefund
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 未关联 / 无权查看：两者对外都是「没有这张退款」，不泄露存在性。
			return entity.TicketRefund{}, false, nil
		}
		return entity.TicketRefund{}, false, err
	}
	return row, true, nil
}

// ListTicketRefundsByTicketIDsWithDB 批量反查一批壳工单对应的退款（列表页用）。
//
// 返回 map[ticketID]refund。列表一页最多 100 行，用一条 IN 查询而不是逐行反查 ——
// 后者在管理台「全部工单」视图下会变成 N+1。
func ListTicketRefundsByTicketIDsWithDB(db *gorm.DB, ticketIDs []int64) (map[int64]entity.TicketRefund, error) {
	result := make(map[int64]entity.TicketRefund, len(ticketIDs))
	if db == nil || len(ticketIDs) == 0 {
		return result, nil
	}
	var rows []entity.TicketRefund
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("ticket_id IN ?", ticketIDs).
		Find(&rows).Error; err != nil {
		return result, err
	}
	for _, row := range rows {
		if row.TicketID != nil {
			result[*row.TicketID] = row
		}
	}
	return result, nil
}

// TicketRefundSummary 是列表行上的退款摘要（只在 category=refund 的行上出现）。
//
// 单独抽一个结构而不是把整行 TicketRefund 塞进列表项：
// 列表只需要「退款到了哪一步、多少钱」，不需要审核人、渠道引用这些内部字段。
type TicketRefundSummary struct {
	RefundID int64 `json:"refundId"`
	// Status 是退款**真实**状态（7 态之一），与工单 status 是两套。
	// 前端必须显示这个而不是工单 status —— 后者是映射出来的展示口径。
	Status string `json:"status"`
	// Kind 是 original（原路退回）/ manual（人工打款）。
	Kind string `json:"kind"`
	// RequestedCents / ApprovedCents 都是分，前端按分转元展示。
	RequestedCents int64  `json:"requestedCents"`
	ApprovedCents  int64  `json:"approvedCents"`
	OrderNo        string `json:"orderNo,omitempty"`
}

// refundSummaryOf 把退款行压成列表摘要。
func refundSummaryOf(row entity.TicketRefund) *TicketRefundSummary {
	return &TicketRefundSummary{
		RefundID:       row.ID,
		Status:         row.Status,
		Kind:           row.Kind,
		RequestedCents: row.RequestedCents,
		ApprovedCents:  row.ApprovedCents,
		OrderNo:        strings.TrimSpace(row.OrderNo),
	}
}

// createRefundTicketWithDB 是「新建工单时选了退款类型」的内部入口（无渠道校验器）。
//
// 仅供测试与内部调用；HTTP 路径一律走 CreateTicketForUserWithDB 并传入 controller
// 解析出来的真实 verifier —— 见该函数的注释（合成一个「永远通过」的桩会让
// 原路退回绕过 fail-closed）。
func createRefundTicketWithDB(db *gorm.DB, user *entity.User, input CreateTicketInput) (TicketDetail, error) {
	return createRefundTicketWithVerifier(db, user, input, nil)
}

// CreateTicketForUserWithDB 是 HTTP 建单的**唯一入口**（T-209）。
//
// 它把「普通工单」与「退款工单」两条路径收在一处，让 controller 只需要传一次
// verify 就能同时满足两边：
//   - ticketType 非 refund → 走原 CreateTicketWithDB（行为与改造前完全一致）；
//   - ticketType = refund  → 走 createRefundTicketWithVerifier（真实落退款行）。
//
// verify 是退款渠道校验器（可为 nil：nil 时原路退回会被 fail-closed 拒绝，
// 这正是「没配置渠道就不允许原路退回」的既有语义）。
func CreateTicketForUserWithDB(db *gorm.DB, user *entity.User, input CreateTicketInput, verify TicketRefundOrderVerifier) (TicketDetail, error) {
	if db == nil || user == nil {
		return TicketDetail{}, ErrTicketForbidden
	}
	ticketType, err := normalizeUserTicketType(input.TicketType)
	if err != nil {
		return TicketDetail{}, err
	}
	if ticketType == TicketTypeRefund {
		return createRefundTicketWithVerifier(db, user, input, verify)
	}
	// 非退款类型却带了退款字段：参数矛盾，明确报错（见 CreateTicketWithDB 的同类判断）。
	if input.RefundOrderID != nil || input.RefundRequestedCents != 0 || strings.TrimSpace(input.RefundReason) != "" {
		return TicketDetail{}, ErrTicketInvalidCategory
	}
	return CreateTicketWithDB(db, user, input)
}

// createRefundTicketWithVerifier 是带渠道校验器的实现（controller 传真实 verifier）。
func createRefundTicketWithVerifier(db *gorm.DB, user *entity.User, input CreateTicketInput, verify TicketRefundOrderVerifier) (TicketDetail, error) {
	if db == nil || user == nil {
		return TicketDetail{}, ErrTicketForbidden
	}
	// 退款申请必须有金额与理由：普通工单的 subject/body 在退款流程里由
	// 壳工单自动生成（订单号 + 退款单号），所以这里校验的是退款字段本身。
	if input.RefundRequestedCents <= 0 {
		return TicketDetail{}, ErrTicketRefundAmount
	}
	reason := strings.TrimSpace(input.RefundReason)
	if reason == "" {
		// 退回「理由必填」：退款是钱的动作，没有理由的申请无法审核。
		return TicketDetail{}, ErrTicketRefundInvalid
	}
	if utf8.RuneCountInString(reason) > 500 || len(reason) > 2000 {
		return TicketDetail{}, ErrTicketRefundInvalid
	}
	kind := entity.TicketRefundKindOriginal
	if input.RefundOrderID == nil {
		// 没有指定订单 = 无单人工退款，必须走 manual（原路退回找不到订单）。
		kind = entity.TicketRefundKindManual
	}
	refund, err := CreateTicketRefundWithDB(db, *user, CreateTicketRefundInput{
		Kind:           kind,
		OrderID:        input.RefundOrderID,
		RequestedCents: input.RefundRequestedCents,
		Reason:         reason,
	}, verify)
	if err != nil {
		return TicketDetail{}, err
	}
	// 壳工单由 CreateTicketRefundWithDB 在同一事务里建好并写回 ticket_id。
	//
	// 走到「没有关联」说明工单表存在、但写入逻辑没把关联写上 —— 那是真实缺陷，
	// 必须让调用方知道（退款本身已落库，不能回滚，但要让用户看到「退款已提交、
	// 工单未生成」而不是一个 200）。
	// 注意：工单表**不存在**的情形不会走到这里 —— CreateTicketRefundShellInTx
	// 在缺表时直接跳过，那种情况下退款依然成功，只是暂时没有壳工单。
	if refund.TicketID == nil || *refund.TicketID <= 0 {
		return TicketDetail{}, ErrTicketRefundTicketLink
	}
	return GetTicketWithDB(db, user, *refund.TicketID)
}
