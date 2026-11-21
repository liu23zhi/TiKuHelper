package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ExportResourceUserBans = "user_bans"
const UserBanImportMaxRows = 200

var ErrUserBanImportStale = errors.New("封禁目标资料或到期时间已变化，请重新预览")

// UserBanImportTarget 只回显按数据库 ID 找到的账号，文件里的用户名等资料不参与匹配。
type UserBanImportTarget struct {
	Row       int        `json:"row"`
	UserID    int32      `json:"userId"`
	Username  string     `json:"username"`
	Nickname  string     `json:"nickname"`
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	Phone     string     `json:"phone"`
	Wechat    string     `json:"wechat"`
	CreatedAt time.Time  `json:"createdAt"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expiresAt"`
	identity  string
}

func userBanImportIdentity(user entity.User) string {
	data, _ := json.Marshal([]any{user.ID, user.Username, user.Nickname, user.Role, user.Status, user.Phone, user.WechatID, user.CreatedAt})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func requireUserBanImportSchema(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(entity.TableNameUser) || !db.Migrator().HasTable(entity.TableNameUserBan) {
		return ErrUserBanDBUnavailable
	}
	return nil
}

func PreviewUserBanImportWithDB(db *gorm.DB, format string, content []byte, now time.Time) ([]UserBanImportTarget, error) {
	if err := requireUserBanImportSchema(db); err != nil {
		return nil, err
	}
	header, rows, err := parseManagementRows(format, content, []string{"user_id", "reason", "expires_at"})
	if err != nil {
		return nil, err
	}
	if len(rows) > UserBanImportMaxRows {
		return nil, fmt.Errorf("单次最多预览 %d 个封禁账号", UserBanImportMaxRows)
	}
	index := importColumnIndex(header)
	if err := requireImportColumns(index, []string{"user_id", "reason"}); err != nil {
		return nil, err
	}
	targets := make([]UserBanImportTarget, 0, len(rows))
	seen := make(map[int32]bool, len(rows))
	for i, row := range rows {
		rowNo := i + 2
		id, err := strconv.ParseInt(importCell(row, index, "user_id"), 10, 32)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("第 %d 行：user_id 必须是有效用户 ID", rowNo)
		}
		if seen[int32(id)] {
			return nil, fmt.Errorf("第 %d 行：用户 ID 重复，请逐一核对", rowNo)
		}
		seen[int32(id)] = true
		reason := importCell(row, index, "reason")
		if reason == "" || utf8.RuneCountInString(reason) > userBanReasonMaxRunes {
			return nil, fmt.Errorf("第 %d 行：封禁原因必填且不超过 255 字", rowNo)
		}
		expiresAt, err := parseRuleTransferTime(importCell(row, index, "expires_at"))
		if err != nil || (expiresAt != nil && !expiresAt.After(now)) {
			return nil, fmt.Errorf("第 %d 行：到期时间无效，必须晚于当前时间（留空表示永久）", rowNo)
		}
		var user entity.User
		if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).First(&user).Error; err != nil {
			return nil, fmt.Errorf("第 %d 行：无法读取用户 ID %d，请核对账号", rowNo, id)
		}
		target := UserBanImportTarget{Row: rowNo, UserID: user.ID, Username: user.Username, Nickname: user.Nickname, Role: user.Role, Status: user.Status, CreatedAt: user.CreatedAt, Reason: reason, ExpiresAt: expiresAt, identity: userBanImportIdentity(user)}
		if user.Phone != nil {
			target.Phone = MaskExportPhone(*user.Phone)
		}
		if user.WechatID != nil {
			target.Wechat = MaskExportWechatID(*user.WechatID)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// ApplyUserBanImportWithDB 在同一事务中重查全部身份快照，再调用既有封禁入口。
// 任一目标资料变化、到期或写入失败都不执行部分封禁。
func ApplyUserBanImportWithDB(db *gorm.DB, targets []UserBanImportTarget, operatorID int32, now time.Time) (int, error) {
	if err := requireUserBanImportSchema(db); err != nil {
		return 0, err
	}
	if len(targets) == 0 || len(targets) > UserBanImportMaxRows || operatorID <= 0 {
		return 0, ErrUserBanInvalid
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		ids := make([]int32, 0, len(targets))
		for _, target := range targets {
			ids = append(ids, target.UserID)
		}
		var users []entity.User
		if err := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", ids).Order("id ASC").Find(&users).Error; err != nil {
			return err
		}
		byID := make(map[int32]entity.User, len(users))
		for _, user := range users {
			byID[user.ID] = user
		}
		for _, target := range targets {
			user, ok := byID[target.UserID]
			if !ok || target.identity == "" || userBanImportIdentity(user) != target.identity {
				return fmt.Errorf("%w：用户 %d", ErrUserBanImportStale, target.UserID)
			}
			if target.ExpiresAt != nil && !target.ExpiresAt.After(now) {
				return fmt.Errorf("%w：用户 %d", ErrUserBanImportStale, target.UserID)
			}
		}
		for _, target := range targets {
			if _, err := Ban(tx, UserBanRequest{UserID: target.UserID, Reason: target.Reason, ExpiresAt: target.ExpiresAt, OperatorUserID: operatorID, Now: now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(targets), nil
}

func init() {
	exportResourceSpecs[ExportResourceUserBans] = exportResourceSpec{
		resource: ExportResourceUserBans,
		columns:  []ExportColumn{exportColumn("id"), exportColumn("user_id"), exportColumn("username"), exportColumn("reason"), exportColumn("banned_at"), exportColumn("expires_at"), exportColumn("banned_by"), exportColumn("created_at")},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var records []struct {
				entity.UserBan
				Username string
			}
			query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBan{}).
				Select("user_bans.*, users.username AS username").Joins("LEFT JOIN users ON users.id = user_bans.user_id").Order("user_bans.id ASC")
			if err := exportLimit(query, limit).Find(&records).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(records))
			for _, row := range records {
				out = append(out, []string{strconv.FormatInt(row.ID, 10), strconv.FormatInt(int64(row.UserID), 10), row.Username, strings.TrimSpace(row.Reason), formatExportTime(row.BannedAt), formatRuleTransferTime(row.ExpiresAt), strconv.FormatInt(int64(row.BannedBy), 10), formatExportTime(row.CreatedAt)})
			}
			return out, nil
		},
	}
}
