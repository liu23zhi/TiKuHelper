package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

const agreementDeviceHeader = "X-Agreement-Device"

var agreementDB = scriptDB

// Registration is the only unauthenticated persistent write. Limit even when
// the optional general API quota is off.
type agreementEnrollmentBudget struct {
	seed    maphash.Seed
	global  *rate.Limiter
	buckets [1024]*rate.Limiter
}

func newAgreementEnrollmentBudget() *agreementEnrollmentBudget {
	b := &agreementEnrollmentBudget{seed: maphash.MakeSeed(), global: rate.NewLimiter(rate.Every(10*time.Second), 12)}
	// Fixed-size buckets avoid retaining every previously observed remote address.
	for i := range b.buckets {
		b.buckets[i] = rate.NewLimiter(rate.Every(5*time.Minute), 3)
	}
	return b
}
func (b *agreementEnrollmentBudget) allow(ip string) bool {
	i := maphash.String(b.seed, ip) % uint64(len(b.buckets))
	return b.buckets[i].Allow() && b.global.Allow()
}

var enrollmentBudget = newAgreementEnrollmentBudget()

func agreementFail(c *gin.Context, op string, err error) {
	code, status := "unavailable", 503
	switch {
	case errors.Is(err, service.ErrAgreementProof):
		code, status = "proof_required", 401
	case errors.Is(err, service.ErrAgreementInput):
		code, status = "invalid_request", 400
	case errors.Is(err, service.ErrAgreementConflict):
		code, status = "version_conflict", 409
	case errors.Is(err, gorm.ErrRecordNotFound):
		code, status = "not_found", 404
	}
	c.JSON(status, gin.H{"operation": op, "code": code})
}
func agreementJSON(c *gin.Context, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 300<<10))
	dec.DisallowUnknownFields()
	if e := dec.Decode(dst); e != nil {
		return service.ErrAgreementInput
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return service.ErrAgreementInput
	}
	return nil
}
func agreementStore(c *gin.Context, op string) (service.AgreementStore, bool) {
	c.Header("Cache-Control", "no-store")
	if (op != "fetch" || c.GetHeader(agreementDeviceHeader) != "" || c.GetHeader("Authorization") != "" || c.GetHeader("X-Script-Identity") != "") && agreementSource(c) == "" {
		c.JSON(503, gin.H{"operation": op, "code": "source_unavailable"})
		return service.AgreementStore{}, false
	}
	db := agreementDB()
	if db == nil {
		agreementFail(c, op, errors.New("no db"))
		return service.AgreementStore{}, false
	}
	return service.AgreementStore{DB: db.WithContext(c.Request.Context())}, true
}
func agreementTLS(c *gin.Context, op string) bool {
	if c.Request.TLS != nil || c.GetBool(scriptProxyHTTPSKey) {
		return true
	}
	c.JSON(403, gin.H{"operation": op, "code": "https_required"})
	return false
}
func agreementProof(c *gin.Context) service.AgreementCredentials {
	return service.AgreementCredentials{Secret: c.GetHeader(agreementDeviceHeader), Authorization: c.GetHeader("Authorization"), Identity: c.GetHeader("X-Script-Identity"), IP: agreementSource(c)}
}
func EnrollAgreementDevice(c *gin.Context) {
	const op = "enroll"
	s, ok := agreementStore(c, op)
	if !ok || !agreementTLS(c, op) {
		return
	}
	if c.Request.ContentLength != 0 || c.Request.URL.RawQuery != "" {
		agreementFail(c, op, service.ErrAgreementInput)
		return
	}
	if !enrollmentBudget.allow(agreementSource(c)) {
		c.Header("Retry-After", "300")
		c.JSON(429, gin.H{"operation": op, "code": "rate_limited"})
		return
	}
	d, secret, e := s.Enroll()
	if e != nil {
		agreementFail(c, op, e)
		return
	}
	c.JSON(201, gin.H{"deviceId": d.ID, "deviceCredential": secret})
}
func CurrentAgreement(c *gin.Context) {
	const op = "fetch"
	s, ok := agreementStore(c, op)
	if !ok {
		return
	}
	var need *bool
	var r entity.AgreementRelease
	var e error
	cred := agreementProof(c)
	if cred.Secret != "" || cred.Authorization != "" || cred.Identity != "" {
		if !agreementTLS(c, op) {
			return
		}
		st, err := s.Status(cred)
		if err != nil {
			agreementFail(c, op, err)
			return
		}
		need = &st.NeedsAcceptance
		e = s.DB.Where("version = ?", st.Version).First(&r).Error
	} else {
		r, e = s.Current()
	}
	if e != nil {
		agreementFail(c, op, e)
		return
	}
	if versions, pinned := c.GetQueryArray("version"); pinned {
		if len(versions) != 1 || versions[0] == "" {
			agreementFail(c, op, service.ErrAgreementInput)
			return
		}
		if versions[0] != r.Version {
			agreementFail(c, op, service.ErrAgreementConflict)
			return
		}
	}
	c.JSON(200, gin.H{"type": "user_agreement", "version": r.Version, "summary": r.Summary, "body": r.Body, "publishedAt": r.PublishedAt, "needsAcceptance": need, "content_hash": service.AgreementContentHash(r.Body)})
}
func AgreementDeviceStatus(c *gin.Context) {
	const op = "status"
	s, ok := agreementStore(c, op)
	if !ok || !agreementTLS(c, op) {
		return
	}
	if c.Request.URL.RawQuery != "" {
		agreementFail(c, op, service.ErrAgreementInput)
		return
	}
	st, e := s.Status(agreementProof(c))
	if e != nil {
		agreementFail(c, op, e)
		return
	}
	c.JSON(200, st)
}
func SubmitAgreement(c *gin.Context) {
	const op = "submit"
	s, ok := agreementStore(c, op)
	if !ok || !agreementTLS(c, op) {
		return
	}
	var in struct {
		Version string `json:"version"`
	}
	if e := agreementJSON(c, &in); e != nil {
		agreementFail(c, op, e)
		return
	}
	r, e := s.Accept(agreementProof(c), in.Version)
	if e != nil {
		agreementFail(c, op, e)
		return
	}
	// No peer/user audit details in client responses.
	c.JSON(200, gin.H{"accepted": true, "version": r.Version, "deviceId": r.DeviceID, "acceptedAt": r.AcceptedAt})
}

type agreementAdminOp func(*gin.Context, service.AgreementStore) (any, error)

func agreementAdmin(name string, action int32, op agreementAdminOp) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireAdmin(c) {
			return
		}
		s, ok := agreementStore(c, name)
		if !ok {
			return
		}
		user, _ := currentOperator(c)
		var out any
		e := s.DB.Transaction(func(tx *gorm.DB) error {
			var err error
			out, err = op(c, service.AgreementStore{DB: tx})
			if err != nil {
				return err
			}
			target := c.Param("id")
			if d, ok := out.(entity.AgreementDraft); ok {
				target = strconv.FormatInt(d.ID, 10)
			}
			if ids, ok := out.([]int64); ok {
				target = fmt.Sprint(ids)
			}
			return service.WriteAccountAuditWithDB(tx, service.AccountAuditEvent{Module: "agreement", Action: action, UserID: user.ID, Username: user.Username, Description: fmt.Sprintf("协议 %s %s", name, target), ClientIP: agreementSource(c), CorrelationID: c.GetString("transport_request_id")})
		})
		if e != nil {
			agreementFail(c, name, e)
			return
		}
		c.JSON(200, out)
	}
}
func agreementID(c *gin.Context) (int64, error) {
	i, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || i <= 0 {
		return 0, service.ErrAgreementInput
	}
	return i, nil
}
func agreementPage(c *gin.Context) (limit, offset int, err error) {
	limit, e := strconv.Atoi(c.DefaultQuery("limit", "25"))
	offset, e2 := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if e != nil || e2 != nil || limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
		return 0, 0, service.ErrAgreementInput
	}
	return
}

var AgreementDrafts = agreementAdmin("list_drafts", logActionSearch, func(c *gin.Context, s service.AgreementStore) (any, error) {
	l, o, e := agreementPage(c)
	if e != nil {
		return nil, e
	}
	var rows []entity.AgreementDraft
	e = s.DB.Omit("body").Order("id DESC").Limit(l).Offset(o).Find(&rows).Error
	return rows, e
})
var AgreementDraftPreview = agreementAdmin("preview", logActionSearch, func(c *gin.Context, s service.AgreementStore) (any, error) {
	id, e := agreementID(c)
	if e != nil {
		return nil, e
	}
	var d entity.AgreementDraft
	e = s.DB.First(&d, id).Error
	return gin.H{"draft": d, "type": "user_agreement", "audience": "all_devices"}, e
})

func agreementDraftInput(c *gin.Context, s service.AgreementStore) (any, error) {
	var in struct {
		entity.AgreementContent
		Revision int64 `json:"revision"`
	}
	if e := agreementJSON(c, &in); e != nil {
		return nil, e
	}
	var id int64
	if c.Request.Method == http.MethodPut {
		var e error
		id, e = agreementID(c)
		if e != nil {
			return nil, e
		}
	}
	return s.SaveDraft(id, in.Revision, in.AgreementContent)
}

var CreateAgreementDraft = agreementAdmin("create", logActionCreate, agreementDraftInput)
var EditAgreementDraft = agreementAdmin("edit", logActionUpdate, agreementDraftInput)
var RemoveAgreementDraft = agreementAdmin("delete", logActionDelete, func(c *gin.Context, s service.AgreementStore) (any, error) {
	id, e := agreementID(c)
	if e != nil {
		return nil, e
	}
	return gin.H{"deleted": id}, s.DeleteDraft(id)
})
var PublishAgreementDraft = agreementAdmin("publish", logActionUpdate, func(c *gin.Context, s service.AgreementStore) (any, error) {
	id, e := agreementID(c)
	if e != nil {
		return nil, e
	}
	var in struct {
		Revision int64 `json:"revision"`
	}
	if e = agreementJSON(c, &in); e != nil {
		return nil, e
	}
	return s.Publish(id, in.Revision)
})
var AgreementHistory = agreementAdmin("history", logActionSearch, func(c *gin.Context, s service.AgreementStore) (any, error) {
	l, o, e := agreementPage(c)
	if e != nil {
		return nil, e
	}
	var rows []entity.AgreementRelease
	e = s.DB.Omit("body").Order("id DESC").Limit(l).Offset(o).Find(&rows).Error
	return rows, e
})
var AgreementReleaseDetail = agreementAdmin("release", logActionSearch, func(c *gin.Context, s service.AgreementStore) (any, error) {
	id, e := agreementID(c)
	if e != nil {
		return nil, e
	}
	var r entity.AgreementRelease
	e = s.DB.First(&r, id).Error
	return r, e
})
var AgreementAuditRecords = agreementAdmin("records", logActionSearch, func(c *gin.Context, s service.AgreementStore) (any, error) {
	l, o, e := agreementPage(c)
	if e != nil {
		return nil, e
	}
	f := service.AgreementFilter{Limit: l, Offset: o, DeviceID: c.Query("deviceId"), ClientKind: c.Query("clientKind"), Version: c.Query("version")}
	if a, ok := c.GetQuery("accepted"); ok {
		if a != "true" && a != "false" {
			return nil, service.ErrAgreementInput
		}
		v := a == "true"
		f.Accepted = &v
	}
	if u, ok := c.GetQuery("userId"); ok {
		if u == "unbound" {
			f.Unbound = true
		} else {
			v, e := strconv.ParseInt(u, 10, 32)
			if e != nil || v <= 0 {
				return nil, service.ErrAgreementInput
			}
			id := int32(v)
			f.UserID = &id
		}
	}
	return s.Records(f)
})

var BatchDeleteAgreementDrafts = agreementAdmin("batch_delete", logActionDelete, func(c *gin.Context, s service.AgreementStore) (any, error) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := agreementJSON(c, &in); err != nil {
		return nil, err
	}
	if len(in.IDs) == 0 || len(in.IDs) > 100 {
		return nil, service.ErrAgreementInput
	}
	seen := make(map[int64]bool, len(in.IDs))
	for _, id := range in.IDs {
		if id <= 0 || seen[id] {
			return nil, service.ErrAgreementInput
		}
		seen[id] = true
	}
	res := s.DB.Where("id IN ?", in.IDs).Delete(&entity.AgreementDraft{})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != int64(len(in.IDs)) {
		return nil, gorm.ErrRecordNotFound
	}
	return in.IDs, nil
})
