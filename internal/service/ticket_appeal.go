package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	TicketAccountIssueBanAppeal = "ban_appeal"
	ticketAppealProofTTL        = 5 * time.Minute
	// Equalizes password verification for unknown usernames and legacy plaintext rows.
	ticketAppealDummyHash = "$2a$10$7qlo76XXV2mVESlY8Q4JoOXK2wqazpjFXix8gJ6OfcR14JptgQsVO"
)

var (
	ErrTicketAppealProof       = errors.New("请重新验证要申诉的封禁账号")
	ErrTicketAppealCredentials = errors.New("无法验证该封禁账号，请核对账号和密码")
	ErrTicketAppealRateLimited = errors.New("验证次数过多，请稍后重试")
	ticketAppealAttemptMu      sync.Mutex
)

type TicketAppealVerification struct {
	Proof        string    `json:"proof"`
	ExpiresAt    time.Time `json:"expiresAt"`
	RegisteredAt time.Time `json:"registeredAt"`
	MaskedPhone  string    `json:"maskedPhone"`
}

type TicketAppealTarget struct {
	ID              int32      `json:"id"`
	Username        string     `json:"username"`
	Nickname        string     `json:"nickname"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	Phone           *string    `json:"phone,omitempty"`
	PhoneBoundAt    *time.Time `json:"phoneBoundAt,omitempty"`
	WechatID        *string    `json:"wechatId,omitempty"`
	WechatBoundAt   *time.Time `json:"wechatBoundAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	RegisteredAt    time.Time  `json:"registeredAt"`
	RegisterSource  string     `json:"registerSource"`
	RegisterIP      string     `json:"registerIp,omitempty"`
	LastLoginAt     *time.Time `json:"lastLoginAt,omitempty"`
	LastLoginIP     string     `json:"lastLoginIp,omitempty"`
	Balance         float64    `json:"balance"`
	GiftBalance     float64    `json:"giftBalance"`
	InvitedByUserID int32      `json:"invitedByUserId"`
	Banned          bool       `json:"banned"`
	BanReason       string     `json:"banReason,omitempty"`
	BanExpiresAt    *time.Time `json:"banExpiresAt,omitempty"`
}

func VerifyTicketAppealAccountWithDB(db *gorm.DB, actor entity.User, username, password, clientIP string) (TicketAppealVerification, error) {
	var output TicketAppealVerification
	if actor.ID <= 0 || strings.TrimSpace(actor.LoginSessionID) == "" {
		return output, ErrTicketForbidden
	}
	now := time.Now().UTC()
	if err := reserveTicketAppealAttempt(db, actor.ID, clientIP, username, now); err != nil {
		return output, err
	}
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 1020 || strings.TrimSpace(password) == "" || util.ValidateBcryptPasswordBytes(password) != nil {
		return output, ErrTicketAppealCredentials
	}
	var target entity.User
	err := db.Session(&gorm.Session{Logger: logger.Discard}).Where("username = ?", username).Take(&target).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return output, err
	}
	matches, _ := util.VerifyPassword(target.Password, password)
	if !strings.HasPrefix(target.Password, "$2a$") && !strings.HasPrefix(target.Password, "$2b$") && !strings.HasPrefix(target.Password, "$2y$") {
		util.VerifyPassword(ticketAppealDummyHash, password)
	}
	if !matches || target.ID == actor.ID {
		return output, ErrTicketAppealCredentials
	}
	banned, err := IsBannedAt(db, target.ID, now)
	if err != nil {
		return output, err
	}
	if !banned {
		return output, ErrTicketAppealCredentials
	}

	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return output, err
	}
	proof := hex.EncodeToString(random[:])
	row := entity.TicketAppealProof{
		ActorID: actor.ID, SessionHash: hashTicketAppealValue(actor.LoginSessionID),
		TargetUserID: target.ID, ProofHash: hashTicketAppealValue(proof), ExpiresAt: now.Add(ticketAppealProofTTL),
		TargetCredentialHash: hashTicketAppealCredential(proof, target.Password),
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "actor_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"session_hash", "target_user_id", "target_credential_hash", "proof_hash", "expires_at"}),
	}).Create(&row).Error; err != nil {
		return output, err
	}
	output = TicketAppealVerification{Proof: proof, ExpiresAt: row.ExpiresAt, RegisteredAt: target.CreatedAt}
	if target.Phone != nil {
		output.MaskedPhone = maskTicketPhone(*target.Phone)
	}
	return output, nil
}

func reserveTicketAppealAttempt(db *gorm.DB, actorID int32, clientIP, username string, now time.Time) error {
	// Login failures and appeal verification have independent counters. Reserve an
	// attempt before bcrypt, including successful requests, to cap concurrent work.
	ticketAppealAttemptMu.Lock()
	defer ticketAppealAttemptMu.Unlock()
	if !db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		return ErrTicketAppealRateLimited
	}
	targetPolicy := AccountLoginPolicy(db)
	targetPolicy.Scope = "appeal_target"
	targetPolicy.Enabled = true
	// Fold case for rate limiting even when the database collation does so for lookup.
	targetKey := strings.ToLower(NormalizeLoginAccountKey(username))
	if targetKey != "" {
		targetKey = hashTicketAppealValue(targetKey)
	}
	for _, limit := range []struct {
		key    string
		policy LoginRateLimitPolicy
	}{
		{strconv.FormatInt(int64(actorID), 10), LoginRateLimitPolicy{Scope: "appeal_actor", Enabled: true, MaxAttempts: 6, WindowMinutes: 15, LockoutMinutes: 15}},
		{NormalizeLoginIPKey(clientIP), LoginRateLimitPolicy{Scope: "appeal_ip", Enabled: true, MaxAttempts: 31, WindowMinutes: 15, LockoutMinutes: 15}},
		{targetKey, targetPolicy},
	} {
		lock, err := CheckLoginLockoutWithDB(db, limit.policy, limit.key, now)
		if err != nil {
			return err
		}
		if lock.Locked {
			return ErrTicketAppealRateLimited
		}
		lock, err = RecordLoginFailureWithDB(db, limit.policy, limit.key, now)
		if err != nil {
			return err
		}
		if lock.Locked {
			return ErrTicketAppealRateLimited
		}
	}
	return nil
}

func hashTicketAppealValue(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func hashTicketAppealCredential(proof, password string) string {
	// Key the verifier with the issued proof so it does not duplicate a legacy password digest.
	hash := hmac.New(sha256.New, []byte(proof))
	hash.Write([]byte(password))
	return hex.EncodeToString(hash.Sum(nil))
}

func resolveTicketAppealTarget(tx *gorm.DB, actor *entity.User, proof string, now time.Time) (int32, error) {
	actorBanned, err := IsBannedAt(tx, actor.ID, now)
	if err != nil {
		return 0, err
	}
	if actorBanned {
		if proof != "" {
			return 0, ErrTicketForbidden
		}
		return actor.ID, nil
	}
	if len(proof) != 64 || actor.LoginSessionID == "" {
		return 0, ErrTicketAppealProof
	}
	query := "actor_id = ? AND session_hash = ? AND proof_hash = ? AND expires_at > ?"
	args := []any{actor.ID, hashTicketAppealValue(actor.LoginSessionID), hashTicketAppealValue(proof), now.UTC()}
	var row entity.TicketAppealProof
	if err := tx.Where(query, args...).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrTicketAppealProof
		}
		return 0, err
	}
	result := tx.Where(query, args...).Delete(&entity.TicketAppealProof{})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, ErrTicketAppealProof
	}
	var target entity.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("password").Take(&target, row.TargetUserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrTicketAppealProof
		}
		return 0, err
	}
	expected := hashTicketAppealCredential(proof, target.Password)
	if !hmac.Equal([]byte(row.TargetCredentialHash), []byte(expected)) {
		return 0, ErrTicketAppealProof
	}
	banned, err := IsBannedAt(tx, row.TargetUserID, now)
	if err != nil {
		return 0, err
	}
	if !banned {
		return 0, ErrTicketAppealProof
	}
	return row.TargetUserID, nil
}

func loadTicketAppealTarget(db *gorm.DB, userID int32) (*TicketAppealTarget, error) {
	var user entity.User
	if err := db.Unscoped().Select(
		"id", "username", "nickname", "role", "status", "phone", "phone_bound_at", "wechat_id", "wechat_bound_at",
		"created_at", "register_source", "register_ip", "last_login_at", "last_login_ip",
		"balance", "gift_balance", "invited_by_user_id", "deleted_at",
	).First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &TicketAppealTarget{ID: userID, Status: "missing"}, nil
		}
		return nil, err
	}
	target := &TicketAppealTarget{
		ID: user.ID, Username: user.Username, Nickname: user.Nickname, Role: user.Role, Status: user.Status,
		Phone: user.Phone, PhoneBoundAt: user.PhoneBoundAt, WechatID: user.WechatID, WechatBoundAt: user.WechatBoundAt,
		CreatedAt: user.CreatedAt, RegisteredAt: user.CreatedAt, RegisterSource: user.RegisterSource, RegisterIP: user.RegisterIP,
		LastLoginAt: user.LastLoginAt, LastLoginIP: user.LastLoginIP, Balance: user.Balance, GiftBalance: user.GiftBalance,
		InvitedByUserID: user.InvitedByUserID,
	}
	if user.DeletedAt.Valid {
		target.Status = "archived"
		return target, nil
	}
	state, err := ticketUserBanState(db, userID, user.Status)
	if err != nil {
		return nil, err
	}
	target.Status, target.Banned = state.Status, state.Banned
	target.BanReason, target.BanExpiresAt = state.BanReason, state.BanExpiresAt
	return target, nil
}

type ticketBanState struct {
	Status       string
	Banned       bool
	BanReason    string
	BanExpiresAt *time.Time
}

func ticketUserBanState(db *gorm.DB, userID int32, status string) (ticketBanState, error) {
	state := ticketBanState{Status: status}
	now := time.Now()
	banned, err := IsBannedAt(db, userID, now)
	if err != nil {
		return state, err
	}
	state.Banned = banned
	if banned {
		state.Status = UserBanStatusBanned
		active, err := ActiveUserBanWithDB(db, userID, now)
		if err != nil {
			return state, err
		}
		if active != nil {
			state.BanReason = active.Reason
			state.BanExpiresAt = active.ExpiresAt
		}
	} else if strings.EqualFold(status, UserBanStatusBanned) {
		state.Status = "enabled"
	}
	return state, nil
}
