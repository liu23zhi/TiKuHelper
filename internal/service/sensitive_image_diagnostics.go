package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

const (
	imageDetectorUnconfigured   = "SENSITIVE_IMAGE_DETECTOR_UNCONFIGURED"
	imageBytesUnavailable       = "SENSITIVE_IMAGE_BYTES_UNAVAILABLE"
	imageReadFailed             = "SENSITIVE_IMAGE_READ_FAILED"
	imageRequestInvalid         = "SENSITIVE_IMAGE_REQUEST_INVALID"
	imageResponseJSONInvalid    = "SENSITIVE_IMAGE_RESPONSE_JSON_INVALID"
	imageResponseVerdictMissing = "SENSITIVE_IMAGE_RESPONSE_VERDICT_MISSING"
	imageUpstreamUnauthorized   = "SENSITIVE_IMAGE_UPSTREAM_UNAUTHORIZED"
	imageUpstreamForbidden      = "SENSITIVE_IMAGE_UPSTREAM_FORBIDDEN"
	imageUpstreamRateLimited    = "SENSITIVE_IMAGE_UPSTREAM_RATE_LIMITED"
	imageUpstreamQuotaExhausted = "SENSITIVE_IMAGE_UPSTREAM_QUOTA_EXHAUSTED"
	imageUpstream4XX            = "SENSITIVE_IMAGE_UPSTREAM_4XX"
	imageUpstream5XX            = "SENSITIVE_IMAGE_UPSTREAM_5XX"
	imageUpstreamTimeout        = "SENSITIVE_IMAGE_UPSTREAM_TIMEOUT"
	imageUpstreamNetwork        = "SENSITIVE_IMAGE_UPSTREAM_NETWORK"
	imageUpstreamUnexpected     = "SENSITIVE_IMAGE_UPSTREAM_UNEXPECTED"
	imageStateSyncFailed        = "SENSITIVE_IMAGE_STATE_SYNC_FAILED"
	imageLocalQuotaReached      = "SENSITIVE_IMAGE_LOCAL_QUOTA_REACHED"
)

// SensitiveImageDiagnostic 是管理端可见的本地故障回执；不携带上游错误或原始请求头。
type SensitiveImageDiagnostic struct {
	Code         string `json:"code"`
	DiagnosticID string `json:"diagnosticId"`
}

func (d SensitiveImageDiagnostic) Error() string { return d.Code + " (" + d.DiagnosticID + ")" }

type imageDetectionTrace struct {
	id, channel, sha, mime        string
	bytes, attachmentID, ticketID int64
	started                       time.Time
	httpStatus                    int
	upstreamRef                   string
}

func startImageDetectionTrace(channel, sha, mime string, size, attachmentID, ticketID int64) *imageDetectionTrace {
	trace := newImageDetectionTrace(channel, sha, mime, size, attachmentID, ticketID)
	trace.write("detect_start", "-", "-")
	return trace
}

func newImageDetectionTrace(channel, sha, mime string, size, attachmentID, ticketID int64) *imageDetectionTrace {
	trace := &imageDetectionTrace{
		id:      "IMG-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		channel: channel, attachmentID: attachmentID, ticketID: ticketID, started: time.Now(),
	}
	trace.setImage(sha, mime, size)
	return trace
}

func (t *imageDetectionTrace) setImage(sha, mime string, size int64) {
	t.sha, t.mime = "-", "-"
	if len(sha) == sha256.Size*2 {
		if _, err := hex.DecodeString(sha); err == nil {
			t.sha = strings.ToLower(sha)
		}
	}
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		t.mime = mime
	}
	t.bytes = size
}

func (t *imageDetectionTrace) setUpstream(status int, rawRequestID string) {
	t.httpStatus = status
	if rawRequestID != "" {
		sum := sha256.Sum256([]byte("tiku-sensitive-image-request-v1:" + rawRequestID))
		t.upstreamRef = hex.EncodeToString(sum[:16])
	}
}

func (t *imageDetectionTrace) write(event, state, code string) {
	ref := t.upstreamRef
	if ref == "" {
		ref = "-"
	}
	logger.SensitiveImageTrace(fmt.Sprintf(
		"event=%s diagnostic_id=%s channel=%s image_sha256=%s attachment_id=%d ticket_id=%d mime=%s bytes=%d state=%s duration_ms=%d http_status=%d upstream_ref=%s code=%s",
		event, t.id, t.channel, t.sha, t.attachmentID, t.ticketID, t.mime, t.bytes, state, time.Since(t.started).Milliseconds(), t.httpStatus, ref, code))
}

func (t *imageDetectionTrace) fail(code string) SensitiveImageDiagnostic {
	t.write("detect_error", "failed", code)
	logger.SysWarn(fmt.Sprintf("sensitive_image_detect_failed diagnostic_id=%s channel=%s code=%s", t.id, t.channel, code))
	return SensitiveImageDiagnostic{Code: code, DiagnosticID: t.id}
}

func (t *imageDetectionTrace) upstreamFailureCode(err error) string {
	var response *uapi.APIError
	if errors.As(err, &response) {
		status := response.HTTPStatus
		t.setUpstream(status, response.RequestID)
		switch {
		case status >= 200 && status < 300 && response.Code == uapi.NSFWResponseJSONInvalid:
			return imageResponseJSONInvalid
		case status >= 200 && status < 300 && response.Code == uapi.NSFWResponseVerdictMissing:
			return imageResponseVerdictMissing
		case status == http.StatusUnauthorized:
			return imageUpstreamUnauthorized
		case status == http.StatusForbidden:
			return imageUpstreamForbidden
		case status == http.StatusPaymentRequired,
			status == http.StatusTooManyRequests && strings.EqualFold(strings.TrimSpace(response.Code), "VISITOR_MONTHLY_QUOTA_EXHAUSTED"):
			return imageUpstreamQuotaExhausted
		case status == http.StatusTooManyRequests:
			return imageUpstreamRateLimited
		case status >= 400 && status < 500:
			return imageUpstream4XX
		case status >= 500 && status < 600:
			return imageUpstream5XX
		}
	}
	switch {
	case errors.Is(err, uapi.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return imageUpstreamTimeout
	case errors.Is(err, uapi.ErrNetwork), errors.Is(err, context.Canceled):
		return imageUpstreamNetwork
	case errors.Is(err, uapi.ErrInvalidArgument):
		return imageRequestInvalid
	default:
		return imageUpstreamUnexpected
	}
}

var localImageDiagnosticID = regexp.MustCompile("^IMG-[0-9a-f]{32}$")

// SafeSensitiveImageHistoryReason 不回放旧版本曾存入缓存的上游原文。
func SafeSensitiveImageHistoryReason(status, reason string) string {
	switch status {
	case "error":
		if code, suffix, ok := strings.Cut(reason, " ("); ok && strings.HasSuffix(suffix, ")") &&
			localImageDiagnosticID.MatchString(strings.TrimSuffix(suffix, ")")) {
			switch code {
			case imageDetectorUnconfigured, imageBytesUnavailable, imageReadFailed, imageRequestInvalid,
				imageResponseJSONInvalid, imageResponseVerdictMissing, imageUpstreamUnauthorized, imageUpstreamForbidden,
				imageUpstreamRateLimited, imageUpstreamQuotaExhausted, imageUpstream4XX, imageUpstream5XX,
				imageUpstreamTimeout, imageUpstreamNetwork, imageUpstreamUnexpected, imageStateSyncFailed:
				return reason
			}
		}
		return "检测失败，请重新检测"
	case "sensitive":
		switch reason {
		case "上游建议 review", "上游建议 block", "上游风险等级 medium", "上游风险等级 high", "历史检测判定敏感", "历史判定", "上游判定为敏感图片":
			return reason
		default:
			return "判定为敏感图片"
		}
	default:
		return ""
	}
}

// 本链路只发固定诊断字段；阻止 ORM 另行回显 SQL 参数或数据库错误正文。
func imageDetectionDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{Logger: db.Logger.LogMode(gormlog.Silent)})
}
