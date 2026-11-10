package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// userBindingRecordSchemaReadyKey 标记「绑定记录表已就绪」。
	userBindingRecordSchemaReadyKey = "user_binding_record_schema_ready"

	// userBindingRecordBackfillBatchSize 控制活跃绑定回填的批大小。
	userBindingRecordBackfillBatchSize = 500
)

// ensureUserBindingRecordSchema 建立 `user_binding_records`（18-*.md §7.2 / 44-*.md §1.2）。
//
// 这张表是三份文档的共同前置：
//   - `18-账号注销与保留区.md`：注销/换绑时把原值搬进来，保留区回看；
//   - `19-二次注册判定与绑定记录.md`：注册时判「这个值以前属于谁」；
//   - `44-二次注册四情形与追缴定案.md`：换绑时用同一份判定触发追缴。
//
// # 最关键的一列：release_reason（判定二分类）
//
// 列 `release_reason` 取值 ” / 'rebind' / 'closure' / 'admin'，与列 `release_source`
// （操作来源 user_unbind/admin_clear/force_reassign/account_close/admin_delete）**是两个字段**。
//
// **缺这一列（或只写 state='released' 不分类）会让 19 号的四个开关退化成一个**：
//   - history_* 需要 release_reason IN ('rebind','admin')
//   - current_* 需要 release_reason = 'closure'
//
// 混在一起时两边命中同一个集合，`current_*` 永远与 `history_*` 同真同假，
// 情形 2/4（用注销释放的值注册 / 换绑）**静默失效且不报错**。
// 因此本迁移除了建表，还要回填已存在行（见 backfillUserBindingReleaseReason）。
//
// 纪律（四条，改这里之前先读）：
//   - 结构性建表/建列失败 → 返回错误，让启动 FATAL 看见；
//   - 数据类回填失败 → 只记日志，不阻断启动，下次启动重试；
//   - system_settings 标记保证只跑一次，失败不写标记；新增列不受标记约束（每次核 HasColumn）；
//   - 分批处理（500/批），绝不按行扫大表；
//   - 不做任何 DROP TABLE。
func ensureUserBindingRecordSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		// users 还没建出来（极端空库首次启动）：交给后续 AutoMigrate，下次启动重试。
		return nil
	}

	// ① 结构性部分：每次都核对（幂等，几次元数据查询），保证老库也能补上后来新增的列。
	applied, err := safeEnsureUserBindingRecordSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}

	// ② 数据类部分：失败只记日志，不阻断启动。
	//    **放在标记判断之前**：标记只防「建表复跑」，不能防「数据没回填完」。
	if err := backfillUserBindingReleaseReason(db); err != nil {
		logger.SysLog(fmt.Sprintf("绑定记录 release_reason 回填失败（下次启动会重试）：%v", err))
	}
	if err := backfillActiveUserBindings(db); err != nil {
		logger.SysLog(fmt.Sprintf("活跃绑定回填失败（下次启动会重试）：%v", err))
	}

	if systemSettingFlagIsSet(db, userBindingRecordSchemaReadyKey) {
		return nil
	}
	if err := setSystemSettingFlag(db, userBindingRecordSchemaReadyKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入绑定记录迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureUserBindingRecordSchema 在外层兜一层 recover：脏数据不能让服务反复崩溃。
func safeEnsureUserBindingRecordSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyUserBindingRecordSchema(db)
}

func applyUserBindingRecordSchema(db *gorm.DB) (bool, error) {
	if err := db.AutoMigrate(&entity.UserBindingRecord{}); err != nil {
		return false, fmt.Errorf("create user_binding_records fail: %w", err)
	}
	// AutoMigrate 之后还要显式核两件事，理由见各自注释。
	if err := ensureUserBindingRecordColumns(db); err != nil {
		return false, err
	}
	if err := ensureUserBindingRecordIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureUserBindingRecordColumns 显式核对关键列存在（尤其是 release_reason）。
//
// 为什么单独核 release_reason：它是 44-*.md 能成立的前提，缺了**不会报错**，
// 只会让四个开关悄悄退化 —— 这种「静默失效」必须在结构层挡住。
func ensureUserBindingRecordColumns(db *gorm.DB) error {
	required := []string{
		"user_id", "kind", "value", "value_hash", "state", "active_key",
		"release_reason", "release_source", "released_at", "bound_at",
	}
	for _, column := range required {
		if db.Migrator().HasColumn(entity.TableNameUserBindingRecord, column) {
			continue
		}
		return fmt.Errorf(
			"user_binding_records 缺少必需列 %s（release_reason 缺失会让二次注册四开关静默退化为一个）", column)
	}
	return nil
}

// ensureUserBindingRecordIndexes 补建两条复合索引。
//
// ⚠️ `idx_binding_active` **必须是唯一索引**：它才是「同一个手机号/微信最多同时绑
// 在一个账号上」的物理保证。缺它时并发绑同一值会静默成功，判定与追缴都会错人。
//
// SQLite 支持 CREATE UNIQUE INDEX IF NOT EXISTS（可幂等重跑）；MySQL 不支持
// IF NOT EXISTS，必须先查再说（与 ensureLoginAttemptIndexes 同构）。
func ensureUserBindingRecordIndexes(db *gorm.DB) error {
	type indexSpec struct {
		name    string
		columns string
		unique  bool
	}
	specs := []indexSpec{
		// 唯一性靠 active_key：released 行为 NULL，多行 NULL 在 MySQL/SQLite 唯一索引里互不冲突。
		{name: "idx_binding_active", columns: "`kind`, `active_key`", unique: true},
		// 判定分流查询 (kind, value_hash, state, release_reason)：
		// 缺这条索引时每次注册/换绑都要两次全表扫描级的 exists。
		{name: "idx_binding_lookup", columns: "`kind`, `value_hash`, `state`, `release_reason`", unique: false},
	}
	dialect := strings.ToLower(strings.TrimSpace(db.Dialector.Name()))
	table := "`" + entity.TableNameUserBindingRecord + "`"
	for _, spec := range specs {
		if db.Migrator().HasIndex(entity.TableNameUserBindingRecord, spec.name) {
			continue
		}
		uniqueKeyword := ""
		if spec.unique {
			uniqueKeyword = "UNIQUE "
		}
		var statement string
		switch dialect {
		case "sqlite":
			statement = fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS `%s` ON %s (%s)",
				uniqueKeyword, spec.name, table, spec.columns)
		case "mysql":
			statement = fmt.Sprintf("CREATE %sINDEX `%s` ON %s (%s)",
				uniqueKeyword, spec.name, table, spec.columns)
		default:
			logger.SysLog(fmt.Sprintf("未知数据库方言 %q，跳过 user_binding_records 索引 %s",
				db.Dialector.Name(), spec.name))
			continue
		}
		if err := db.Exec(statement).Error; err != nil {
			if db.Migrator().HasIndex(entity.TableNameUserBindingRecord, spec.name) {
				continue
			}
			return fmt.Errorf("create index %s on %s fail: %w", spec.name, entity.TableNameUserBindingRecord, err)
		}
	}
	return nil
}

// backfillUserBindingReleaseReason 修正「state=released 但 release_reason 为空」的历史行。
//
// 为什么需要：本表在 44-*.md 之前的设计里只有操作来源（release_source）。若某版本
// 曾按旧写法写入过 released 行，那些行 release_reason 为空 —— 判定时
// **两边都不命中**（既不是 history 也不是 current），情形 2/4 会静默漏掉它们。
//
// 映射规则（18-*.md §7.2 方案 B + 44-*.md §5.1）：
//
//	account_close / admin_delete            → 'closure'（注销时释放，归 current_*）
//	user_unbind / admin_clear / force_reassign → 'rebind' （换绑/清空释放，归 history_*）
//	空或未知来源                            → 'rebind' （安全侧默认：归历史）
//
// 为什么未知来源默认归历史而不是丢弃：'closure' 会让 current_* 命中、影响「是否赠送」，
// 而 'rebind' 只影响 history_*；把无法判定的行放进 history 是**保守**方向 ——
// 既不放松「不二次赠送」，也不会把陌生的旧来源当成「用户注销时仍持有」。
//
// 复杂度：两次 UPDATE（各一条 SQL），与行数无关。
func backfillUserBindingReleaseReason(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return nil
	}
	var pending int64
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Where("state = ? AND COALESCE(release_reason, '') = ''", entity.BindingStateReleased).
		Count(&pending).Error; err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	closureSources := []string{
		entity.BindingReleaseSourceAccountClose,
		entity.BindingReleaseSourceAdminDelete,
	}
	updatedClosure := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Where("state = ? AND COALESCE(release_reason, '') = '' AND release_source IN ?",
			entity.BindingStateReleased, closureSources).
		Update("release_reason", entity.BindingReleaseReasonClosure)
	if updatedClosure.Error != nil {
		return updatedClosure.Error
	}
	updatedRest := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Where("state = ? AND COALESCE(release_reason, '') = ''", entity.BindingStateReleased).
		Update("release_reason", entity.BindingReleaseReasonRebind)
	if updatedRest.Error != nil {
		return updatedRest.Error
	}
	logger.SysLog(fmt.Sprintf(
		"绑定记录 release_reason 回填：共 %d 条待分类，其中 closure=%d、rebind（含未知来源，安全侧默认）=%d",
		pending, updatedClosure.RowsAffected, updatedRest.RowsAffected))
	return nil
}

// userBindingBackfillRow 是回填时从 users 表读出的最小列集。
type userBindingBackfillRow struct {
	ID            int32      `gorm:"column:id"`
	Username      string     `gorm:"column:username"`
	Phone         *string    `gorm:"column:phone"`
	PhoneBoundAt  *time.Time `gorm:"column:phone_bound_at"`
	WechatID      *string    `gorm:"column:wechat_id"`
	WechatBoundAt *time.Time `gorm:"column:wechat_bound_at"`
}

// backfillActiveUserBindings 把 users 上**当前仍绑着**的手机号/微信补成 active 绑定行。
//
// 边界（18-*.md §7.3）：
//   - **只回填「现在仍绑着的」**（users.phone / wechat_id 非空）；
//   - 已经解绑过、或被 forceReassign 清空的历史值在库里**没有任何痕迹**，无法回填，
//     不假装能回填 —— 那正是本表从今天起才有的能力；
//   - 已存在同值 active 行 → 跳过（幂等重跑）。
//
// 复杂度（性能契约，改前先看）：
//   - users 表按 id 游标**只扫一遍**，分批 500（不用 OFFSET，避免深分页退化）；
//   - 每批一次 IN 查询取「已占用的 value_hash 集合」，**不逐行查**；
//   - 每批插入时跳过已被占用的值。总复杂度 O(用户数)，内存 O(批大小)。
func backfillActiveUserBindings(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return nil
	}
	var lastID int32
	total := 0
	for {
		rows := make([]userBindingBackfillRow, 0, userBindingRecordBackfillBatchSize)
		if err := db.Session(&gorm.Session{NewDB: true}).Table(entity.TableNameUser).
			Select("id, username, phone, phone_bound_at, wechat_id, wechat_bound_at").
			Where("id > ?", lastID).
			Order("id asc").Limit(userBindingRecordBackfillBatchSize).
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		lastID = rows[len(rows)-1].ID
		created, err := backfillActiveBindingsForBatch(db, rows)
		if err != nil {
			return err
		}
		total += created
		if len(rows) < userBindingRecordBackfillBatchSize {
			break
		}
	}
	if total > 0 {
		logger.SysLog(fmt.Sprintf(
			"活跃绑定回填：新增 %d 条 state=active 绑定记录（只含当前仍绑着的值；历史上已释放的值无法回填）", total))
	}
	return nil
}

// backfillActiveBindingsForBatch 处理一批用户：一次 IN 查已占用值，一次批量插入。
func backfillActiveBindingsForBatch(db *gorm.DB, rows []userBindingBackfillRow) (int, error) {
	candidates := make([]entity.UserBindingRecord, 0, len(rows)*2)
	hashes := make([]string, 0, len(rows)*2)
	appendCandidate := func(userID int32, username, kind, value string, boundAt *time.Time) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		hash := bindingBackfillHash(value)
		hashes = append(hashes, hash)
		candidates = append(candidates, entity.UserBindingRecord{
			UserID:           userID,
			UsernameSnapshot: username,
			Kind:             kind,
			Value:            value,
			ValueHash:        hash,
			State:            entity.BindingStateActive,
			ActiveKey:        &hash,
			Source:           "backfill",
			BoundAt:          boundAt,
		})
	}
	for i := range rows {
		row := rows[i]
		if row.Phone != nil {
			appendCandidate(row.ID, row.Username, entity.BindingKindPhone, *row.Phone, row.PhoneBoundAt)
		}
		if row.WechatID != nil {
			appendCandidate(row.ID, row.Username, entity.BindingKindWechat, *row.WechatID, row.WechatBoundAt)
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	// 一次 IN 查询取本批里已被占用的 (kind, value_hash)：
	// 只扫这一批涉及的值，与表总行数无关（走 idx_binding_lookup）。
	type occupiedKey struct {
		Kind      string `gorm:"column:kind"`
		ValueHash string `gorm:"column:value_hash"`
	}
	var occupied []occupiedKey
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Select("kind, value_hash").
		Where("value_hash IN ? AND state = ?", hashes, entity.BindingStateActive).
		Scan(&occupied).Error; err != nil {
		return 0, err
	}
	taken := make(map[string]struct{}, len(occupied))
	for _, key := range occupied {
		taken[key.Kind+"|"+key.ValueHash] = struct{}{}
	}
	pending := make([]entity.UserBindingRecord, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		dedupeKey := candidate.Kind + "|" + candidate.ValueHash
		if _, exists := taken[dedupeKey]; exists {
			continue
		}
		if _, dup := seen[dedupeKey]; dup {
			// 同一批内同值（users 表唯一索引本该挡住，兜一层防脏数据）。
			continue
		}
		seen[dedupeKey] = struct{}{}
		pending = append(pending, candidate)
	}
	if len(pending) == 0 {
		return 0, nil
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Create(&pending).Error; err != nil {
		return 0, err
	}
	return len(pending), nil
}

// bindingBackfillHash 与 service.BindingValueHash 必须**完全一致**（sha256(TrimSpace(value))）。
//
// 为什么不 import service：registry 被 service 依赖（service → registry/dbqueue），
// 反向 import 会成环。两处实现一致性靠 review + 迁移测试钉住：
// 测试里会用 users.phone 造一行 active 记录，再用 service.BindingValueHash 去查，
// 查不到就说明两边漂移了。
func bindingBackfillHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}
