package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	WechatRegisterStatusPending = "pending"
	WechatRegisterStatusStaged  = "staged"
	WechatRegisterStatusExpired = "expired"
	wechatRegisterFlowIdleTTL   = 5 * time.Minute
)

var (
	ErrWechatRegisterFlowInvalid   = errors.New("注册流程令牌无效")
	ErrWechatRegisterFlowExpired   = errors.New("注册流程已过期")
	ErrWechatRegisterQRInvalid     = errors.New("微信二维码会话无效或已过期")
	ErrWechatRegisterQRCodeExpired = errors.New("微信二维码已过期")
	ErrWechatRegisterNotStaged     = errors.New("注册流程尚未暂存微信")
	ErrWechatRegisterAlreadyStaged = errors.New("注册流程已暂存微信")
)

// WechatRegisterQRCode contains only data that is safe to expose to the public
// registration page. The upstream login token is intentionally represented as
// an opaque session ID and is never returned under the login API's field name.
type WechatRegisterQRCode struct {
	SessionID     string    `json:"sessionId"`
	DisplayData   string    `json:"displayData,omitempty"`
	ExpiresAt     time.Time `json:"-"`
	ExpiresAtUnix int64     `json:"expiresAtUnix"`
}

type WechatRegisterFlowSnapshot struct {
	Token         string                `json:"registrationFlowToken"`
	FlowID        string                `json:"flowId"`
	Status        string                `json:"status"`
	QRCode        *WechatRegisterQRCode `json:"qrcode,omitempty"`
	ExpiresAtUnix int64                 `json:"expiresAtUnix"`
	Staged        bool                  `json:"staged"`
}

type wechatRegisterFlow struct {
	mu         sync.Mutex
	tokenHash  string
	flowID     string
	createdAt  time.Time
	lastSeenAt time.Time
	expiresAt  time.Time
	currentQR  *WechatRegisterQRCode
	stagedID   string
	released   bool
}

type WechatRegisterFlowStore struct {
	mu            sync.Mutex
	flows         map[string]*wechatRegisterFlow
	sessions      map[string]string
	cleanupStop   chan struct{}
	cleanupDone   chan struct{}
	cleanupOnce   *sync.Once
	cleanupActive bool
}

// StartCleanup starts at most one background cleanup loop for this store.
// The returned function is safe to call repeatedly and waits for the loop to
// stop, which keeps tests and graceful shutdowns deterministic.
func (s *WechatRegisterFlowStore) StartCleanup(interval time.Duration) func() {
	s = s.ensure()
	if interval <= 0 {
		interval = time.Minute
	}
	s.mu.Lock()
	if s.cleanupActive {
		stopCh, doneCh, once := s.cleanupStop, s.cleanupDone, s.cleanupOnce
		s.mu.Unlock()
		return func() {
			once.Do(func() { close(stopCh) })
			<-doneCh
		}
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	once := &sync.Once{}
	s.cleanupStop = stopCh
	s.cleanupDone = doneCh
	s.cleanupOnce = once
	s.cleanupActive = true
	s.mu.Unlock()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(doneCh)
		defer func() {
			s.mu.Lock()
			if s.cleanupStop == stopCh {
				s.cleanupActive = false
				s.cleanupStop = nil
				s.cleanupDone = nil
				s.cleanupOnce = nil
			}
			s.mu.Unlock()
		}()
		for {
			select {
			case now := <-ticker.C:
				s.CleanupExpired(now)
			case <-stopCh:
				return
			}
		}
	}()
	return func() {
		once.Do(func() { close(stopCh) })
		<-doneCh
	}
}

func NewWechatRegisterFlowStore() *WechatRegisterFlowStore {
	return &WechatRegisterFlowStore{
		flows:    make(map[string]*wechatRegisterFlow),
		sessions: make(map[string]string),
	}
}

var DefaultWechatRegisterFlowStore = NewWechatRegisterFlowStore()

type WechatRegisterQRCodeStatus struct {
	Status   string
	WechatID string
}

// PollWechatRegisterQRCode reuses the WeChat upstream status endpoint while
// keeping the result separate from ordinary login/JWT resolution.
func PollWechatRegisterQRCode(client WechatLoginClient, sessionID string) (WechatRegisterQRCodeStatus, error) {
	upstream, err := fetchWechatUpstreamStatus(client, sessionID)
	if err != nil {
		return WechatRegisterQRCodeStatus{}, err
	}
	return WechatRegisterQRCodeStatus{Status: upstream.Status, WechatID: upstream.WechatID}, nil
}

func hashWechatRegisterToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func (s *WechatRegisterFlowStore) ensure() *WechatRegisterFlowStore {
	if s == nil {
		return DefaultWechatRegisterFlowStore
	}
	return s
}

func (s *WechatRegisterFlowStore) find(token string) (*wechatRegisterFlow, string, error) {
	s = s.ensure()
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, "", ErrWechatRegisterFlowInvalid
	}
	hash := hashWechatRegisterToken(token)
	s.mu.Lock()
	flow := s.flows[hash]
	s.mu.Unlock()
	if flow == nil {
		return nil, hash, ErrWechatRegisterFlowInvalid
	}
	return flow, hash, nil
}

func (s *WechatRegisterFlowStore) CreateOrResume(token string, now time.Time) (WechatRegisterFlowSnapshot, error) {
	s = s.ensure()
	token = strings.TrimSpace(token)
	if token != "" {
		flow, hash, err := s.find(token)
		if err != nil {
			return WechatRegisterFlowSnapshot{}, err
		}
		if flow != nil {
			flow.mu.Lock()
			if !flow.released && now.Before(flow.expiresAt) {
				s.invalidateExpiredQRCodeLocked(flow, now)
				flow.lastSeenAt = now
				flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
				snapshot := flow.snapshot(token)
				flow.mu.Unlock()
				return snapshot, nil
			}
			flow.released = true
			s.removeLocked(hash, flow)
			flow.mu.Unlock()
			return WechatRegisterFlowSnapshot{Status: WechatRegisterStatusExpired}, ErrWechatRegisterFlowExpired
		}
	}
	newToken := randomTokenHex(32)
	newHash := hashWechatRegisterToken(newToken)
	flow := &wechatRegisterFlow{
		tokenHash:  newHash,
		flowID:     randomTokenHex(16),
		createdAt:  now,
		lastSeenAt: now,
		expiresAt:  now.Add(wechatRegisterFlowIdleTTL),
	}
	s.mu.Lock()
	s.flows[newHash] = flow
	s.mu.Unlock()
	flow.mu.Lock()
	snapshot := flow.snapshot(newToken)
	flow.mu.Unlock()
	return snapshot, nil
}

func (s *WechatRegisterFlowStore) GetOrCreateQRCode(token string, now time.Time, refresh bool, factory func() (WechatRegisterQRCode, error)) (WechatRegisterQRCode, error) {
	s = s.ensure()
	flow, hash, err := s.find(token)
	if err != nil {
		return WechatRegisterQRCode{}, err
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.released || !now.Before(flow.expiresAt) {
		flow.released = true
		s.removeLocked(hash, flow)
		return WechatRegisterQRCode{}, ErrWechatRegisterFlowExpired
	}
	if !refresh && flow.currentQR != nil && now.Before(flow.currentQR.ExpiresAt) {
		flow.lastSeenAt = now
		flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
		return *flow.currentQR, nil
	}
	if factory == nil {
		return WechatRegisterQRCode{}, errors.New("二维码创建函数为空")
	}
	qr, err := factory()
	if err != nil {
		return WechatRegisterQRCode{}, err
	}
	qr.SessionID = strings.TrimSpace(qr.SessionID)
	if qr.SessionID == "" || !qr.ExpiresAt.After(now) {
		return WechatRegisterQRCode{}, ErrWechatRegisterQRInvalid
	}
	if flow.currentQR != nil {
		s.mu.Lock()
		delete(s.sessions, flow.currentQR.SessionID)
		s.mu.Unlock()
	}
	flow.currentQR = &qr
	flow.lastSeenAt = now
	flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
	s.mu.Lock()
	s.sessions[qr.SessionID] = hash
	s.mu.Unlock()
	return qr, nil
}

func (s *WechatRegisterFlowStore) StatusByQRCodeSession(sessionID string, now time.Time) (WechatRegisterFlowSnapshot, error) {
	s = s.ensure()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return WechatRegisterFlowSnapshot{}, ErrWechatRegisterQRInvalid
	}
	s.mu.Lock()
	hash := s.sessions[sessionID]
	flow := s.flows[hash]
	s.mu.Unlock()
	if flow == nil || hash == "" {
		return WechatRegisterFlowSnapshot{}, ErrWechatRegisterQRInvalid
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.released || flow.currentQR == nil || flow.currentQR.SessionID != sessionID {
		return WechatRegisterFlowSnapshot{}, ErrWechatRegisterQRInvalid
	}
	if !now.Before(flow.expiresAt) || !now.Before(flow.currentQR.ExpiresAt) {
		if now.Before(flow.expiresAt) && !now.Before(flow.currentQR.ExpiresAt) {
			s.invalidateExpiredQRCodeLocked(flow, now)
			flow.lastSeenAt = now
			flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
			if strings.TrimSpace(flow.stagedID) != "" {
				return flow.snapshot(""), nil
			}
			return WechatRegisterFlowSnapshot{Status: WechatRegisterStatusExpired, ExpiresAtUnix: flow.expiresAt.UnixMilli()}, nil
		}
		flow.released = true
		s.removeLocked(hash, flow)
		return WechatRegisterFlowSnapshot{Status: WechatRegisterStatusExpired}, ErrWechatRegisterFlowExpired
	}
	flow.lastSeenAt = now
	flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
	return flow.snapshot(""), nil
}

func (s *WechatRegisterFlowStore) ExpireQRCodeSession(sessionID string, now time.Time) (WechatRegisterFlowSnapshot, error) {
	s = s.ensure()
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	hash := s.sessions[sessionID]
	flow := s.flows[hash]
	s.mu.Unlock()
	if flow == nil || hash == "" {
		return WechatRegisterFlowSnapshot{}, ErrWechatRegisterQRInvalid
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.released || flow.currentQR == nil || flow.currentQR.SessionID != sessionID {
		return WechatRegisterFlowSnapshot{}, ErrWechatRegisterQRInvalid
	}
	s.invalidateQRCodeLocked(flow)
	flow.lastSeenAt = now
	flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
	if strings.TrimSpace(flow.stagedID) != "" {
		return flow.snapshot(""), nil
	}
	return WechatRegisterFlowSnapshot{Status: WechatRegisterStatusExpired, ExpiresAtUnix: flow.expiresAt.UnixMilli()}, ErrWechatRegisterQRCodeExpired
}

func (s *WechatRegisterFlowStore) Status(token string, now time.Time) (WechatRegisterFlowSnapshot, error) {
	s = s.ensure()
	flow, hash, err := s.find(token)
	if err != nil {
		return WechatRegisterFlowSnapshot{}, err
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.released || !now.Before(flow.expiresAt) {
		flow.released = true
		s.removeLocked(hash, flow)
		return WechatRegisterFlowSnapshot{Status: WechatRegisterStatusExpired}, ErrWechatRegisterFlowExpired
	}
	flow.lastSeenAt = now
	flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
	s.invalidateExpiredQRCodeLocked(flow, now)
	return flow.snapshot(strings.TrimSpace(token)), nil
}

func (s *WechatRegisterFlowStore) Stage(token, wechatID string, now time.Time) error {
	s = s.ensure()
	flow, hash, err := s.find(token)
	if err != nil {
		return err
	}
	wechatID = strings.TrimSpace(wechatID)
	if wechatID == "" {
		return errors.New("微信用户 ID 为空")
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.released || !now.Before(flow.expiresAt) {
		flow.released = true
		s.removeLocked(hash, flow)
		return ErrWechatRegisterFlowExpired
	}
	flow.stagedID = wechatID
	flow.lastSeenAt = now
	flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
	return nil
}

// StageByCode serializes external code resolution and staging under the flow lock.
func (s *WechatRegisterFlowStore) StageByCode(token string, now time.Time, resolve func() (string, error)) error {
	s = s.ensure()
	flow, hash, err := s.find(token)
	if err != nil {
		return err
	}
	if resolve == nil {
		return errors.New("微信验证码解析函数为空")
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.released || !now.Before(flow.expiresAt) {
		flow.released = true
		s.removeLocked(hash, flow)
		return ErrWechatRegisterFlowExpired
	}
	if strings.TrimSpace(flow.stagedID) != "" {
		return ErrWechatRegisterAlreadyStaged
	}
	wechatID, err := resolve()
	if err != nil {
		return err
	}
	wechatID = strings.TrimSpace(wechatID)
	if wechatID == "" {
		return errors.New("微信用户 ID 为空")
	}
	flow.stagedID = wechatID
	flow.lastSeenAt = now
	flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
	return nil
}

func (s *WechatRegisterFlowStore) StageByQRCodeSession(sessionID, wechatID string, now time.Time) bool {
	s = s.ensure()
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	hash := s.sessions[sessionID]
	flow := s.flows[hash]
	s.mu.Unlock()
	if flow == nil || hash == "" {
		return false
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.released || flow.currentQR == nil || flow.currentQR.SessionID != sessionID || !now.Before(flow.currentQR.ExpiresAt) || !now.Before(flow.expiresAt) {
		return false
	}
	if strings.TrimSpace(flow.stagedID) != "" {
		return false
	}
	wechatID = strings.TrimSpace(wechatID)
	if wechatID == "" {
		return false
	}
	flow.stagedID = wechatID
	flow.lastSeenAt = now
	flow.expiresAt = now.Add(wechatRegisterFlowIdleTTL)
	return true
}

func (s *WechatRegisterFlowStore) Consume(token string, now time.Time, consume func(string) error) error {
	s = s.ensure()
	flow, hash, err := s.find(token)
	if err != nil {
		return err
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.released || !now.Before(flow.expiresAt) {
		flow.released = true
		s.removeLocked(hash, flow)
		return ErrWechatRegisterFlowExpired
	}
	if strings.TrimSpace(flow.stagedID) == "" {
		return ErrWechatRegisterNotStaged
	}
	if consume == nil {
		return errors.New("注册流程消费函数为空")
	}
	if err := consume(flow.stagedID); err != nil {
		return err
	}
	flow.released = true
	s.removeLocked(hash, flow)
	return nil
}

func (s *WechatRegisterFlowStore) Release(token string) error {
	s = s.ensure()
	flow, hash, err := s.find(token)
	if err != nil {
		return nil
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	flow.released = true
	s.removeLocked(hash, flow)
	return nil
}

func (s *WechatRegisterFlowStore) CleanupExpired(now time.Time) int {
	s = s.ensure()
	s.mu.Lock()
	items := make([]struct {
		hash string
		flow *wechatRegisterFlow
	}, 0, len(s.flows))
	for hash, flow := range s.flows {
		items = append(items, struct {
			hash string
			flow *wechatRegisterFlow
		}{hash: hash, flow: flow})
	}
	s.mu.Unlock()
	removed := 0
	for _, item := range items {
		item.flow.mu.Lock()
		if !item.flow.released && !now.Before(item.flow.expiresAt) {
			item.flow.released = true
			s.removeLocked(item.hash, item.flow)
			removed++
		} else {
			s.invalidateExpiredQRCodeLocked(item.flow, now)
		}
		item.flow.mu.Unlock()
	}
	return removed
}

func (s *WechatRegisterFlowStore) invalidateExpiredQRCodeLocked(flow *wechatRegisterFlow, now time.Time) {
	if flow.currentQR == nil || now.Before(flow.currentQR.ExpiresAt) {
		return
	}
	s.invalidateQRCodeLocked(flow)
}

func (s *WechatRegisterFlowStore) invalidateQRCodeLocked(flow *wechatRegisterFlow) {
	if flow.currentQR == nil {
		return
	}
	s.mu.Lock()
	delete(s.sessions, flow.currentQR.SessionID)
	s.mu.Unlock()
	flow.currentQR = nil
}

func (s *WechatRegisterFlowStore) removeLocked(hash string, flow *wechatRegisterFlow) {
	s = s.ensure()
	s.mu.Lock()
	if current := s.flows[hash]; current == flow {
		delete(s.flows, hash)
		for sessionID, ownerHash := range s.sessions {
			if ownerHash == hash {
				delete(s.sessions, sessionID)
			}
		}
	}
	s.mu.Unlock()
}

func (f *wechatRegisterFlow) snapshot(token string) WechatRegisterFlowSnapshot {
	snapshot := WechatRegisterFlowSnapshot{
		Token:         token,
		FlowID:        f.flowID,
		Status:        WechatRegisterStatusPending,
		ExpiresAtUnix: f.expiresAt.UnixMilli(),
		Staged:        strings.TrimSpace(f.stagedID) != "",
	}
	if snapshot.Staged {
		snapshot.Status = WechatRegisterStatusStaged
	}
	if f.currentQR != nil {
		qr := *f.currentQR
		snapshot.QRCode = &qr
	}
	return snapshot
}
