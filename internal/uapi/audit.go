package uapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

type providerAuditContextKey struct{}
type webWorkflowAuditContextKey struct{}

// The provider header is arbitrary, so an unkeyed digest can still expose
// low-entropy values. This key stays in memory: the optional provider hint
// can be correlated within one process lifetime, not across restarts.
var providerAuditHashKey, providerAuditHashKeyError = uuid.NewRandom()

type providerAudit struct {
	category       string
	workflowID     string
	id             string
	started        time.Time
	attempts       int
	httpStatus     int
	upstreamIDHash string
}

type providerAuditEvent struct {
	Phase          string         `json:"phase"`
	Category       string         `json:"category"`
	ID             string         `json:"request_id"`
	WorkflowID     string         `json:"workflow_id,omitempty"`
	UpstreamIDHash string         `json:"upstream_id_hash,omitempty"`
	HTTPStatus     int            `json:"http_status,omitempty"`
	Attempts       int            `json:"attempts"`
	Retries        int            `json:"retries"`
	ElapsedMS      int64          `json:"elapsed_ms"`
	Outcome        string         `json:"outcome,omitempty"`
	ErrorClass     string         `json:"error_class,omitempty"`
	Summary        map[string]int `json:"summary,omitempty"`
}

// Audit a logical endpoint call, after its typed response has been parsed.
// Do not log a URL, query, body, error text, account key, or upstream field
// value here. Upstream request IDs are untrusted and must never be printed
// verbatim. Use the existing local retention/rotation pipeline.
func auditProviderCall[T any](ctx context.Context, category string, call func(context.Context) (T, error)) (T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id, idErr := uuid.NewRandom()
	if idErr != nil {
		// Correlation failure must not change an upstream operation.
		return call(ctx)
	}
	workflowID, _ := ctx.Value(webWorkflowAuditContextKey{}).(string)
	a := &providerAudit{
		category:   category,
		workflowID: workflowID,
		id:         id.String(),
		started:    time.Now(),
	}
	a.publish(providerAuditEvent{Phase: "start"})
	reply, err := call(context.WithValue(ctx, providerAuditContextKey{}, a))
	end := providerAuditEvent{Phase: "finish", ElapsedMS: time.Since(a.started).Milliseconds()}
	if err != nil {
		end.ErrorClass = auditErrorClass(err, a.httpStatus)
		end.Outcome = "error"
		if end.ErrorClass == "cancelled" || end.ErrorClass == "timeout" {
			end.Outcome = end.ErrorClass
		}
	} else {
		end.Outcome, end.Summary = auditResponseShape(reply)
		switch end.Outcome {
		case "empty_result", "invalid_response":
			end.ErrorClass = end.Outcome
		}
		if a.attempts == 0 {
			end.Outcome = "no_request"
		}
	}
	a.publish(end)
	return reply, err
}

func (a *providerAudit) publish(event providerAuditEvent) {
	event.Category = a.category
	event.ID = a.id
	event.WorkflowID = a.workflowID
	event.HTTPStatus = a.httpStatus
	event.UpstreamIDHash = a.upstreamIDHash
	event.Attempts = a.attempts
	event.Retries = max(0, a.attempts-1)
	// The schema contains only controlled enum strings, local IDs, integers
	// and fixed map keys. Ignoring a logging failure does not alter the call.
	if data, err := json.Marshal(event); err == nil {
		logger.SysLog("uapi_call " + string(data))
	}
}

// Count actual calls into the transport, not the configured retry allowance.
// If a transport returns an HTTP response we retain the last response status;
// a later attempted transport without a response resets that observation.
func noteProviderAttempt(ctx context.Context) {
	if a, _ := ctx.Value(providerAuditContextKey{}).(*providerAudit); a != nil {
		a.attempts++
		a.httpStatus = 0
		a.upstreamIDHash = ""
	}
}

func noteProviderResponse(ctx context.Context, status int, upstreamID string) {
	if a, _ := ctx.Value(providerAuditContextKey{}).(*providerAudit); a != nil {
		a.httpStatus = status
		a.upstreamIDHash = hashProviderRequestID(upstreamID)
	}
}

func hashProviderRequestID(id string) string {
	if id == "" || providerAuditHashKeyError != nil {
		return ""
	}
	digest := hmac.New(sha256.New, providerAuditHashKey[:])
	_, _ = digest.Write([]byte(id))
	return hex.EncodeToString(digest.Sum(nil))
}

// ProviderFailure is a projection for adjacent diagnostics, never a replacement
// for the typed error returned to the caller. Only an actual APIError supplies
// HTTP status and the hash of an untrusted provider correlation header.
type ProviderFailure struct {
	Class          string
	HTTPStatus     int
	UpstreamIDHash string
}

func DescribeProviderFailure(err error) ProviderFailure {
	result := ProviderFailure{}
	var api *APIError
	if errors.As(err, &api) && api != nil {
		if api.HTTPStatus >= 100 && api.HTTPStatus <= 599 {
			result.HTTPStatus = api.HTTPStatus
		}
		result.UpstreamIDHash = hashProviderRequestID(api.RequestID)
	}
	result.Class = auditErrorClass(err, result.HTTPStatus)
	return result
}

func auditErrorClass(err error, status int) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrInsufficientCredits):
		return "credits_exhausted"
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrTextTooLong):
		return "text_too_long"
	case errors.Is(err, ErrInvalidArgument):
		return "invalid_argument"
	case errors.Is(err, ErrNetwork):
		return "transport"
	case status >= 500:
		return "upstream_5xx"
	case status >= 400:
		return "upstream_4xx"
	case status >= 200 && status < 300:
		// Unmarshal, missing required property or invalid typed response.
		return "invalid_response"
	default:
		return "upstream"
	}
}

func hasAuditField(value string) int {
	if strings.TrimSpace(value) != "" {
		return 1
	}
	return 0
}

func auditResponseShape(reply any) (string, map[string]int) {
	switch v := reply.(type) {
	case PhoneInfo:
		outcome := "success"
		if hasAuditField(v.City)+hasAuditField(v.Province)+hasAuditField(v.SP) == 0 {
			outcome = "empty_result"
		}
		return outcome, map[string]int{"has_city": hasAuditField(v.City), "has_province": hasAuditField(v.Province), "has_operator": hasAuditField(v.SP), "has_area_code": hasAuditField(v.AreaCode)}
	case IPInfo:
		outcome := "success"
		if hasAuditField(v.Region) == 0 && LocationDisplayOperator(v.LLC, v.ISP) == "" {
			outcome = "empty_result"
		}
		return outcome, map[string]int{"has_ip": hasAuditField(v.IP), "has_region": hasAuditField(v.Region), "has_asn": hasAuditField(v.ASN), "segment_boundary_fields": hasAuditField(v.BeginIP) + hasAuditField(v.EndIP)}
	case ImageOCRResponse:
		return "success", map[string]int{"has_text": hasAuditField(v.Text), "has_markdown": hasAuditField(v.Markdown), "word_rows": len(v.WordsResult)}
	case ProfanityCheckResponse:
		outcome := "clear"
		if v.Forbidden() {
			outcome = "matched"
		} else if !IsDefinitiveProfanityStatus(v.Status) {
			outcome = "invalid_response"
		}
		return outcome, map[string]int{"matched_count": len(v.ForbiddenWords), "has_status": hasAuditField(v.Status)}
	case SearchEnginesResponse:
		return "success", map[string]int{"has_engine": hasAuditField(v.Engine.Name), "supported_parameter_count": len(v.SupportedParameters)}
	case SearchAggregateResponse:
		return "success", map[string]int{"result_count": len(v.Results), "source_count": len(v.Sources)}
	case StatusUsageResponse:
		return "success", map[string]int{"endpoint_count": len(v.Endpoints), "has_selected_path": hasAuditField(v.Path)}
	case WebToMarkdownTask:
		// Enum allowlist only: provider-controlled strings must not reach the log.
		outcome := "unknown_state"
		switch v.Status {
		case "completed", "failed", "timeout", "cancelled", "queued", "pending", "processing":
			outcome = v.Status
		}
		hasResult := 0
		if v.hasMarkdown() {
			hasResult = 1
		} else if outcome == "completed" {
			outcome = "empty_result"
		}
		return outcome, map[string]int{"has_result": hasResult, "has_task": hasAuditField(v.TaskID)}
	default:
		return "success", nil
	}
}

// This event describes ReadWebPage's terminal business outcome, not another
// egress request. It deliberately has no category, attempts or retries.
func finishWebWorkflow(id string, started time.Time, task WebToMarkdownTask, err error) {
	if id == "" {
		return
	}
	outcome, class := "completed", ""
	if err != nil {
		outcome = "error"
		class = DescribeProviderFailure(err).Class
		switch {
		case class == "cancelled" || class == "timeout":
			outcome = class
		case task.Status == "completed" && !task.hasMarkdown():
			outcome, class = "empty_result", "empty_result"
		case task.Status == "timeout" || task.Status == "cancelled" || task.Status == "failed":
			outcome, class = task.Status, "provider_task_"+task.Status
		}
	}
	event := struct {
		Workflow     string `json:"workflow"`
		ID           string `json:"workflow_id"`
		Phase        string `json:"phase"`
		ProviderCall bool   `json:"provider_call"`
		ElapsedMS    int64  `json:"elapsed_ms"`
		Outcome      string `json:"outcome"`
		ErrorClass   string `json:"error_class,omitempty"`
	}{"web_read", id, "finish", false, time.Since(started).Milliseconds(), outcome, class}
	if data, err := json.Marshal(event); err == nil {
		logger.SysLog("uapi_workflow " + string(data))
	}
}
