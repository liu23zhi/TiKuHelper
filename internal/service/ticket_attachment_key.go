package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ticketAttachmentEncryptionKeySetting = "ticket_attachment_encryption_key"

// 图片功能始终可用；此错误仅表示密钥存储或显式密钥配置损坏。
var ErrTicketAttachmentEncryption = errors.New("工单图片加密暂不可用")

// InitializeTicketAttachmentEncryptionWithDB 在启动时准备持久密钥，避免首次上传才初始化。
func InitializeTicketAttachmentEncryptionWithDB(db *gorm.DB) error {
	_, err := ticketAttachmentCipher(db)
	return err
}

func ticketAttachmentCipher(db *gorm.DB) (cipher.AEAD, error) {
	key, err := resolveTicketAttachmentKey(db)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTicketAttachmentEncryption, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTicketAttachmentEncryption, err)
	}
	return cipher.NewGCM(block)
}

// resolveTicketAttachmentKey 兼容既有环境密钥；未配置时生成独立的随机密钥。
// 密钥与数据库一起持久化，多实例首次启动通过唯一键竞争后读取同一胜出值。
// 已保存的密钥不得因坏配置或随机重启被替换，否则历史图片会永久无法解密。
func resolveTicketAttachmentKey(db *gorm.DB) ([]byte, error) {
	if db == nil {
		return nil, errors.New("数据库未初始化")
	}
	raw := strings.TrimSpace(os.Getenv("TIKU_TICKET_ATTACHMENT_KEY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("TIKU_REFUND_PROOF_KEY"))
	}
	var configured []byte
	if raw != "" {
		var err error
		configured, err = decodeTicketAttachmentKey(raw)
		if err != nil {
			return nil, errors.New("环境中的图片密钥必须为 32 字节的 Base64")
		}
	}

	setting, found, err := getSystemSettingByKey(db, ticketAttachmentEncryptionKeySetting)
	if err != nil {
		return nil, fmt.Errorf("读取图片密钥失败: %w", err)
	}
	if !found {
		key := configured
		if len(key) == 0 {
			key = make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return nil, fmt.Errorf("生成图片密钥失败: %w", err)
			}
		}
		setting = entity.SystemSetting{
			Key: ticketAttachmentEncryptionKeySetting, Value: base64.StdEncoding.EncodeToString(key),
			Remark: "工单图片持久加密密钥（请随数据库备份，勿更改）",
		}
		if err := db.Session(&gorm.Session{NewDB: true}).
			Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).
			Create(&setting).Error; err != nil {
			return nil, fmt.Errorf("保存图片密钥失败: %w", err)
		}
		// 另一个进程可能先写入；必须重新读取，不能使用本次未落库的随机值。
		setting, _, err = getSystemSettingByKey(db, ticketAttachmentEncryptionKeySetting)
		if err != nil {
			return nil, fmt.Errorf("读取图片密钥失败: %w", err)
		}
	}
	stored, err := decodeTicketAttachmentKey(strings.TrimSpace(setting.Value))
	if err != nil {
		return nil, errors.New("已保存的图片密钥无效，需从数据库备份恢复")
	}
	if len(configured) > 0 && string(configured) != string(stored) {
		return nil, errors.New("环境图片密钥与已保存密钥不一致，请恢复原配置")
	}
	return stored, nil
}

func decodeTicketAttachmentKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid image encryption key")
	}
	return key, nil
}
