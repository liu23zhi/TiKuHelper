package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestRechargeOrderListReturnsScopedRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listRechargeOrders
	defer func() {
		listRechargeOrders = original
	}()

	paidAt := time.Date(2026, 6, 20, 13, 0, 0, 0, time.UTC)
	listRechargeOrders = func(user entity.User, page int, pageSize int) (service.RechargeOrderListResult, error) {
		if user.ID != 31 || user.Role != "user" {
			t.Fatalf("unexpected user: %+v", user)
		}
		if page != 2 || pageSize != 5 {
			t.Fatalf("expected pagination 2/5, got %d/%d", page, pageSize)
		}
		return service.RechargeOrderListResult{
			Data: []service.RechargeOrderListItem{{
				ID:                        9,
				UserID:                    user.ID,
				Username:                  "member",
				OrderNo:                   "rcg_listed",
				PaymentProvider:           service.DefaultPaymentProvider,
				PaymentMethodNameSnapshot: "微信支付",
				Amount:                    18.8,
				Status:                    "paid",
				ThirdPartyOrderNo:         "trade-1",
				PaidAt:                    &paidAt,
			}},
			Total:    1,
			Page:     page,
			PageSize: pageSize,
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/recharge-orders?page=2&pageSize=5", nil)
	ctx.Set("user", &entity.User{ID: 31, Role: "user", Status: "enabled"})

	RechargeOrderList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response service.RechargeOrderListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 1 || len(response.Data) != 1 || response.Data[0].OrderNo != "rcg_listed" {
		t.Fatalf("unexpected recharge order list response: %+v", response)
	}
	if response.Data[0].Username != "member" || response.Data[0].PaidAt == nil {
		t.Fatalf("expected username and paid metadata, got %+v", response.Data[0])
	}
}

func TestRechargeOrderListNormalizesInvalidPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listRechargeOrders
	defer func() {
		listRechargeOrders = original
	}()

	listRechargeOrders = func(user entity.User, page int, pageSize int) (service.RechargeOrderListResult, error) {
		if page != 1 || pageSize != 10 {
			t.Fatalf("expected normalized pagination 1/10, got %d/%d", page, pageSize)
		}
		return service.RechargeOrderListResult{Data: []service.RechargeOrderListItem{}, Total: 0, Page: page, PageSize: pageSize}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/recharge-orders?page=0&pageSize=1000", nil)
	ctx.Set("user", &entity.User{ID: 32, Role: "user", Status: "enabled"})

	RechargeOrderList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestRechargeOrderListReturnsServerErrorOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listRechargeOrders
	defer func() {
		listRechargeOrders = original
	}()

	listRechargeOrders = func(user entity.User, page int, pageSize int) (service.RechargeOrderListResult, error) {
		return service.RechargeOrderListResult{}, errors.New("boom")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/recharge-orders", nil)
	ctx.Set("user", &entity.User{ID: 33, Role: "user", Status: "enabled"})

	RechargeOrderList(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateRechargeOrderRejectsInvalidPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/recharge-orders", bytes.NewBufferString("not-json"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "user", Status: "enabled"})

	CreateRechargeOrder(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateRechargeOrderReturnsCreatedOrderAndQRCodePaymentInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createRechargeOrder
	defer func() {
		createRechargeOrder = original
	}()

	createRechargeOrder = func(user entity.User, input service.CreateRechargeOrderInput) (service.CreateRechargeOrderResult, error) {
		if user.ID != 21 || user.Role != "user" {
			t.Fatalf("unexpected user: %+v", user)
		}
		if input.Amount != 12.35 || input.PaymentMethodID != 3 {
			t.Fatalf("unexpected input: %+v", input)
		}
		if input.ClientIP == "" || input.Device != "wechat" {
			t.Fatalf("expected controller to enrich client ip and wechat device, got %+v", input)
		}
		return service.CreateRechargeOrderResult{
			Order: entity.RechargeOrder{
				ID:                        5,
				UserID:                    user.ID,
				OrderNo:                   "rcg_created",
				PaymentProvider:           service.DefaultPaymentProvider,
				PaymentMethodNameSnapshot: "支付宝",
				Amount:                    12.35,
				Status:                    "pending",
			},
			PayType:       "qrcode",
			PayInfo:       "weixin://wxpay/bizpayurl?pr=04IPMKM",
			QRCodeContent: "weixin://wxpay/bizpayurl?pr=04IPMKM",
		}, nil
	}

	payload := []byte(`{"amount":12.35,"paymentMethodId":3}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/recharge-orders", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("User-Agent", "Mozilla/5.0 iPhone MicroMessenger")
	ctx.Set("user", &entity.User{ID: 21, Role: "user", Status: "enabled"})

	CreateRechargeOrder(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var result service.CreateRechargeOrderResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.PayType != "qrcode" || result.QRCodeContent != "weixin://wxpay/bizpayurl?pr=04IPMKM" {
		t.Fatalf("unexpected payment info: %+v", result)
	}
	if result.Order.UserID != 21 || result.Order.PaymentMethodNameSnapshot != "支付宝" {
		t.Fatalf("unexpected created order: %+v", result.Order)
	}
}

func TestCreateRechargeOrderReturnsNotFoundForUnknownPaymentMethod(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createRechargeOrder
	defer func() {
		createRechargeOrder = original
	}()

	createRechargeOrder = func(user entity.User, input service.CreateRechargeOrderInput) (service.CreateRechargeOrderResult, error) {
		return service.CreateRechargeOrderResult{}, service.ErrRechargePaymentMethodNotFound
	}

	payload := []byte(`{"amount":20,"paymentMethodId":999}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/recharge-orders", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 2, Role: "user", Status: "enabled"})

	CreateRechargeOrder(ctx)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestRechargePaymentNotifyReturnsSuccessText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := handleRechargePaymentNotify
	defer func() {
		handleRechargePaymentNotify = original
	}()

	handleRechargePaymentNotify = func(values url.Values) (bool, error) {
		if values.Get("out_trade_no") != "rcg_notify_success" || values.Get("trade_status") != "TRADE_SUCCESS" {
			t.Fatalf("unexpected notify values: %+v", values)
		}
		return true, nil
	}

	body := "out_trade_no=rcg_notify_success&trade_status=TRADE_SUCCESS&sign=mock"
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/payment/notify", bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	RechargePaymentNotify(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != "success" {
		t.Fatalf("expected success body, got %q", recorder.Body.String())
	}
}

func TestRechargePaymentNotifyAcceptsGetQueryCallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := handleRechargePaymentNotify
	defer func() {
		handleRechargePaymentNotify = original
	}()

	handleRechargePaymentNotify = func(values url.Values) (bool, error) {
		if values.Get("out_trade_no") != "rcg_notify_get" || values.Get("trade_status") != "TRADE_SUCCESS" || values.Get("timestamp") != "1781972343" {
			t.Fatalf("unexpected notify values: %+v", values)
		}
		return true, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/payment/notify?out_trade_no=rcg_notify_get&trade_status=TRADE_SUCCESS&timestamp=1781972343&sign=mock&sign_type=RSA", nil)

	RechargePaymentNotify(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != "success" {
		t.Fatalf("expected success body, got %q", recorder.Body.String())
	}
}

func TestRechargePaymentNotifyReturnsFailTextWhenSettlementFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := handleRechargePaymentNotify
	defer func() {
		handleRechargePaymentNotify = original
	}()

	handleRechargePaymentNotify = func(values url.Values) (bool, error) {
		return false, errors.New("boom")
	}

	body := "out_trade_no=rcg_notify_fail&trade_status=TRADE_SUCCESS&sign=mock"
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/payment/notify", bytes.NewBufferString(body))
	ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	RechargePaymentNotify(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != "fail" {
		t.Fatalf("expected fail body, got %q", recorder.Body.String())
	}
}
