package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

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

func TestCreateRechargeOrderReturnsCreatedOrderAndPayURL(t *testing.T) {
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
			PayURL: "https://pay.example.com/api/pay/submit?mock=1",
		}, nil
	}

	payload := []byte(`{"amount":12.35,"paymentMethodId":3}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/recharge-orders", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 21, Role: "user", Status: "enabled"})

	CreateRechargeOrder(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var result service.CreateRechargeOrderResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.PayURL != "https://pay.example.com/api/pay/submit?mock=1" {
		t.Fatalf("unexpected pay url: %q", result.PayURL)
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
