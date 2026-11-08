package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

var (
	ErrTicketRefundChannelDisabled = errors.New("未启用或未核验原支付退款渠道")
	ErrTicketRefundChannelUnknown  = errors.New("支付渠道结果未确认，冻结金保持不变")
	refundGatewayIDPattern         = regexp.MustCompile(`^[0-9A-Za-z_-]{1,128}$`)
	refundGatewayAmountPattern     = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,2})?$`)
)

// TicketRefundEPayConfig identifies the explicitly opted-in, signed pid/money
// V2 profile. Other "易支付" implementations need their own verified adapter.
type TicketRefundEPayConfig struct {
	gatewayURL, partnerID, platformKey, merchantKey, fingerprint string
}

func LoadTicketRefundEPayConfigWithDB(db *gorm.DB) (TicketRefundEPayConfig, error) {
	if db == nil || os.Getenv("TIKU_REFUND_EPAY_PROFILE") != "rsa-pid-money-v2" {
		return TicketRefundEPayConfig{}, ErrTicketRefundChannelDisabled
	}
	settings, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		return TicketRefundEPayConfig{}, err
	}
	cfg := TicketRefundEPayConfig{
		gatewayURL: refundGatewayBase(settings.EPayGatewayURL), partnerID: strings.TrimSpace(settings.EPayPartnerID),
		platformKey: strings.TrimSpace(settings.EPayPlatformPublicKey), merchantKey: strings.TrimSpace(settings.EPayMerchantPrivateKey),
	}
	cfg.fingerprint = refundChannelFingerprint(cfg.gatewayURL, cfg.partnerID, cfg.platformKey)
	private, err := parseRechargePrivateKey(cfg.merchantKey)
	if cfg.fingerprint == "" || err != nil || private.N.BitLen() < 2048 {
		return TicketRefundEPayConfig{}, ErrTicketRefundChannelDisabled
	}
	return cfg, nil
}

func refundGatewayBase(raw string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Port() != "" && u.Port() != "443") {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	// Restrict to DNS names; do not allow explicit loopback, IPs or
	// alternative authority encodings to choose an unexpected network.
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return ""
	}
	u.Host = host
	return u.String()
}

func validRefundGatewayID(raw string) bool { return refundGatewayIDPattern.MatchString(raw) }

func refundChannelFingerprint(gateway, partner, platform string) string {
	base := refundGatewayBase(gateway)
	if base == "" || !validRefundGatewayID(partner) {
		return ""
	}
	key, err := parseRechargePublicKey(platform)
	if err != nil || key.N.BitLen() < 2048 {
		return ""
	}
	der := x509.MarshalPKCS1PublicKey(key)
	sum := sha256.Sum256(append([]byte("refund/rsa-pid-money-v2\x00"+base+"\x00"+partner+"\x00"), der...))
	return hex.EncodeToString(sum[:])
}

func (cfg TicketRefundEPayConfig) VerifyOriginalOrder(_ *gorm.DB, order entity.RechargeOrder) (string, error) {
	if cfg.fingerprint == "" || order.RefundChannelFingerprint != cfg.fingerprint ||
		order.PaymentProvider != DefaultPaymentProvider || !validRefundGatewayID(order.OrderNo) ||
		!validRefundGatewayID(order.RefundOriginalTradeNo) {
		return "", ErrTicketRefundOrderUnavailable
	}
	return cfg.fingerprint, nil
}

func parseRefundGatewayCents(value string) (int64, bool) {
	if !refundGatewayAmountPattern.MatchString(value) {
		return 0, false
	}
	parts := strings.SplitN(value, ".", 2)
	yuan, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	var fraction int64
	if len(parts) == 2 {
		fraction, _ = strconv.ParseInt(parts[1], 10, 64)
		if len(parts[1]) == 1 {
			fraction *= 10
		}
	}
	return yuan*100 + fraction, true
}

var refundEPayHTTPClient = &http.Client{
	Timeout: 12 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || port != "443" {
				return nil, ErrTicketRefundChannelDisabled
			}
			resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(resolved) == 0 {
				return nil, ErrTicketRefundChannelUnknown
			}
			for _, ip := range resolved {
				if !ip.IP.IsGlobalUnicast() || IsPrivateOrReservedIP(ip.IP.String()) {
					return nil, ErrTicketRefundChannelDisabled
				}
			}
			dialer := net.Dialer{Timeout: 5 * time.Second}
			return dialer.DialContext(ctx, network, net.JoinHostPort(resolved[0].IP.String(), port))
		},
	},
}

// SubmitTicketRefundEPay sends one authorized attempt. Even a valid submit
// acknowledgement is not proof of actual refund success; only query settles.
func SubmitTicketRefundEPay(ctx context.Context, cfg TicketRefundEPayConfig, refund entity.TicketRefund, order entity.RechargeOrder) error {
	if !refundEPayMatches(cfg, refund, order) || refund.Status != TicketRefundStatusProcessing {
		return ErrTicketRefundChannelDisabled
	}
	values := url.Values{
		"trade_no":      {order.RefundOriginalTradeNo},
		"out_refund_no": {refund.RefundNo},
		"money":         {fmt.Sprintf("%d.%02d", refund.ApprovedCents/100, refund.ApprovedCents%100)},
	}
	reply, err := requestRefundEPay(ctx, cfg, "/api/pay/refund", values)
	if err != nil || !matchesRefundEPayReply(cfg, refund, order, reply) {
		return ErrTicketRefundChannelUnknown
	}
	return nil
}

// QueryTicketRefundEPay binds a terminal result to the signed refund ID,
// original trade, merchant scope and exact cents. Not-found and ambiguous
// replies are not evidence that no money moved.
func QueryTicketRefundEPay(ctx context.Context, cfg TicketRefundEPayConfig, refund entity.TicketRefund, order entity.RechargeOrder) (string, string, error) {
	if !refundEPayMatches(cfg, refund, order) ||
		(refund.Status != TicketRefundStatusProcessing && refund.Status != TicketRefundStatusUnknown) {
		return "", "", ErrTicketRefundChannelDisabled
	}
	values := url.Values{"out_refund_no": {refund.RefundNo}}
	reply, err := requestRefundEPay(ctx, cfg, "/api/pay/refundquery", values)
	if err != nil || !matchesRefundEPayReply(cfg, refund, order, reply) || reply.Get("out_trade_no") != order.OrderNo {
		return "", "", ErrTicketRefundChannelUnknown
	}
	switch reply.Get("status") {
	case "1":
		return TicketRefundStatusSucceeded, reply.Get("refund_no"), nil
	case "0":
		return TicketRefundStatusFailed, reply.Get("refund_no"), nil
	default:
		return "", "", ErrTicketRefundChannelUnknown
	}
}

func refundEPayMatches(cfg TicketRefundEPayConfig, refund entity.TicketRefund, order entity.RechargeOrder) bool {
	if refund.OrderID == nil || *refund.OrderID != order.ID || order.UserID != refund.UserID ||
		refund.Kind != TicketRefundKindOriginal || refund.ChannelRef != cfg.fingerprint ||
		order.OrderNo != refund.OrderNo || order.RefundOriginalTradeNo != refund.OriginalTradeNo ||
		refund.ApprovedCents <= 0 || !validRefundGatewayID(refund.RefundNo) {
		return false
	}
	_, err := cfg.VerifyOriginalOrder(nil, order)
	return err == nil
}

func matchesRefundEPayReply(cfg TicketRefundEPayConfig, refund entity.TicketRefund, order entity.RechargeOrder, values url.Values) bool {
	amount, ok := parseRefundGatewayCents(values.Get("money"))
	pid := values.Get("pid")
	return values.Get("code") == "0" && ok && amount == refund.ApprovedCents &&
		values.Get("out_refund_no") == refund.RefundNo && values.Get("trade_no") == order.RefundOriginalTradeNo &&
		validRefundGatewayID(values.Get("refund_no")) && (pid == "" || pid == cfg.partnerID)
}

func requestRefundEPay(ctx context.Context, cfg TicketRefundEPayConfig, path string, values url.Values) (url.Values, error) {
	values.Set("pid", cfg.partnerID)
	values.Set("timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	sig, err := signRechargeValues(values, cfg.merchantKey)
	if err != nil {
		return nil, ErrTicketRefundChannelDisabled
	}
	values.Set("sign", sig)
	values.Set("sign_type", rechargeOrderSignType)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.gatewayURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, ErrTicketRefundChannelDisabled
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := refundEPayHTTPClient.Do(req)
	if err != nil {
		return nil, ErrTicketRefundChannelUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrTicketRefundChannelUnknown
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, ErrTicketRefundChannelUnknown
	}
	reply, err := parseRefundGatewayFields(raw)
	if err != nil || reply.Get("sign_type") != rechargeOrderSignType || !verifyRechargeValuesSignature(reply, cfg.platformKey) {
		return nil, ErrTicketRefundChannelUnknown
	}
	ts, err := strconv.ParseInt(reply.Get("timestamp"), 10, 64)
	now := time.Now().Unix()
	if err != nil || ts < now-300 || ts > now+300 {
		return nil, ErrTicketRefundChannelUnknown
	}
	return reply, nil
}

func parseRefundGatewayFields(data []byte) (url.Values, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return nil, ErrTicketRefundChannelUnknown
	}
	fields := url.Values{}
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		if err != nil || !ok || fields.Has(name) || len(fields) >= 64 {
			return nil, ErrTicketRefundChannelUnknown
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, ErrTicketRefundChannelUnknown
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			var n json.Number
			if json.Unmarshal(raw, &n) != nil {
				return nil, ErrTicketRefundChannelUnknown
			}
			value = n.String()
		}
		fields.Set(name, value)
	}
	last, err := dec.Token()
	if err != nil || last != json.Delim('}') {
		return nil, ErrTicketRefundChannelUnknown
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrTicketRefundChannelUnknown
	}
	return fields, nil
}
