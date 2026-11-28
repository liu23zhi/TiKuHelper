package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/extensionlogs"
	"github.com/itihey/tikuAdapter/internal/service"
)

const diagnosticReceiptTransport = "receipt-v1"

// Legacy senders do not carry an agreement proof or a stable receipt. Tell a
// legitimate sender to stop instead of accepting ungated or unrepeatable writes.
func LegacyDiagnosticPolicy(c *gin.Context) {
	header, ok := diagnosticBearer(c)
	if !ok {
		return
	}
	if _, ok = diagnosticOwner(c, header, c.Query("mode")); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"enabled": false})
}

func LegacyDiagnosticWrite(c *gin.Context) {
	if _, ok := diagnosticBearer(c); !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusForbidden, gin.H{"enabled": false, "code": "receipt_transport_required"})
}

func diagnosticGrant(c *gin.Context, mode string, use func(int32) error) (error, bool) {
	if !agreementTLS(c, "diagnostic_receipt") {
		return nil, false
	}
	if _, ok := diagnosticBearer(c); !ok {
		return nil, false
	}
	if !extensionlogs.KnownMode(mode) {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_mode"})
		return nil, false
	}
	cred := agreementProof(c)
	if (mode == extensionlogs.ModeExam) != (cred.Identity != "") {
		return service.ErrAgreementProof, true
	}
	db := diagnosticDB()
	if db == nil {
		return errors.New("diagnostic database unavailable"), true
	}
	c.Header("Cache-Control", "no-store")
	s := service.AgreementStore{DB: db.WithContext(c.Request.Context())}
	return s.WithDiagnosticConsent(cred, use), true
}

func DiagnosticReceiptPolicy(c *gin.Context) {
	var enabled bool
	err, ok := diagnosticGrant(c, c.Query("mode"), func(_ int32) error {
		p, e := extensionLogFiles.Policy()
		enabled = e == nil && p.Enabled
		return e
	})
	if !ok {
		return
	}
	if err != nil && !errors.Is(err, service.ErrDiagnosticConsent) {
		diagnosticReceiptError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": enabled, "transport": diagnosticReceiptTransport})
}

type diagnosticPacket struct {
	extensionlogs.Session
	extensionlogs.ReceiptBatch
}
type diagnosticReceiptResponse struct {
	Scope       extensionlogs.Session `json:"scope"`
	BatchID     string                `json:"batch_id"`
	AcceptedIDs []string              `json:"accepted_event_ids"`
}

func UploadDiagnosticReceiptBatch(c *gin.Context) {
	if !agreementTLS(c, "diagnostic_receipt") {
		return
	}
	if _, ok := diagnosticBearer(c); !ok {
		return
	}
	var b diagnosticPacket
	if !readLogJSON(c, &b, 64<<10) {
		return
	}
	var proof extensionlogs.ReceiptProof
	err, ok := diagnosticGrant(c, b.Mode, func(owner int32) error {
		var e error
		proof, e = extensionLogFiles.AppendReceipt(c.Request.Context(), owner, b.Session, b.ReceiptBatch)
		return e
	})
	if !ok {
		return
	}
	if err != nil {
		diagnosticReceiptError(c, err)
		return
	}
	c.JSON(http.StatusOK, diagnosticReceiptResponse{b.Session, proof.BatchID, proof.AcceptedEventIDs})
}

func diagnosticReceiptError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAgreementProof):
		c.JSON(http.StatusUnauthorized, gin.H{"code": "proof_required"})
	case errors.Is(err, service.ErrDiagnosticConsent), errors.Is(err, extensionlogs.ErrDisabled):
		c.JSON(http.StatusForbidden, gin.H{"enabled": false, "code": "diagnostic_disabled"})
	case errors.Is(err, extensionlogs.ErrBatch):
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_batch"})
	case errors.Is(err, extensionlogs.ErrReceiptConflict):
		c.JSON(http.StatusConflict, gin.H{"code": "receipt_conflict"})
	case errors.Is(err, extensionlogs.ErrReceiptCapacity):
		c.JSON(http.StatusTooManyRequests, gin.H{"code": "diagnostic_capacity"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "diagnostic_unavailable"})
	}
}

func diagnosticReceiptFilter(c *gin.Context) (extensionlogs.Filter, bool) {
	f := extensionlogs.Filter{Mode: c.Query("mode"), DeviceID: c.Query("device_id"), BrowserID: c.Query("browser_id"), LaunchID: c.Query("launch_id")}
	if raw, ok := c.GetQuery("user_id"); ok {
		n, e := strconv.ParseInt(raw, 10, 32)
		if e != nil || n < 1 {
			return f, false
		}
		f.UserID = int32(n)
	}
	return f, f.Valid()
}

func AdminDiagnosticReceiptLogs(c *gin.Context) {
	f, ok := diagnosticReceiptFilter(c)
	n, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if !ok || err != nil || n < 1 || n > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_filter"})
		return
	}
	rows, next, err := extensionLogFiles.ReadReceipts(c.Request.Context(), f, n, c.Query("cursor"))
	if errors.Is(err, extensionlogs.ErrCursor) {
		c.JSON(http.StatusGone, gin.H{"code": "diagnostic_cursor_expired"})
		return
	}
	if err != nil {
		diagnosticReceiptError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"entries": rows, "next_cursor": next})
}

func AdminExportDiagnosticReceiptLogs(c *gin.Context) {
	f, ok := diagnosticReceiptFilter(c)
	if !ok || c.Query("cursor") != "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_filter"})
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	cursor := ""
	rowsCount := 0
	for {
		rows, next, err := extensionLogFiles.ReadReceipts(c.Request.Context(), f, 100, cursor)
		if errors.Is(err, extensionlogs.ErrCursor) {
			c.JSON(http.StatusGone, gin.H{"code": "diagnostic_cursor_expired"})
			return
		}
		if err != nil {
			diagnosticReceiptError(c, err)
			return
		}
		rowsCount += len(rows)
		if rowsCount > 5000 {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"code": "narrow_diagnostic_filter"})
			return
		}
		for _, row := range rows {
			if err = enc.Encode(row); err != nil {
				diagnosticReceiptError(c, err)
				return
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", `attachment; filename="diagnostic-receipts.jsonl"`)
	c.Data(http.StatusOK, "application/x-ndjson", buf.Bytes())
}
