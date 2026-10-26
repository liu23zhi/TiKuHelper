package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestPaymentMethodListReturnsScopedMethods(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listPaymentMethods
	defer func() {
		listPaymentMethods = original
	}()

	listPaymentMethods = func(user entity.User) ([]entity.PaymentMethod, error) {
		if user.ID != 7 || user.Role != "user" {
			t.Fatalf("unexpected user: %+v", user)
		}
		return []entity.PaymentMethod{{ID: 1, Name: "支付宝", EPayType: "alipay", Enabled: true, Sort: 10}}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/payment-methods", nil)
	ctx.Set("user", &entity.User{ID: 7, Role: "user", Status: "enabled"})

	PaymentMethodList(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var methods []entity.PaymentMethod
	if err := json.Unmarshal(recorder.Body.Bytes(), &methods); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(methods) != 1 || methods[0].Name != "支付宝" {
		t.Fatalf("unexpected payment methods: %+v", methods)
	}
}

func TestCreatePaymentMethodRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/payment-methods", bytes.NewBufferString(`{"name":"支付宝","epayType":"alipay"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 8, Role: "user", Status: "enabled"})

	CreatePaymentMethod(ctx)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreatePaymentMethodReturnsCreatedItemForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createPaymentMethod
	defer func() {
		createPaymentMethod = original
	}()

	createPaymentMethod = func(user entity.User, input service.CreatePaymentMethodInput) (entity.PaymentMethod, error) {
		if user.ID != 9 || user.Role != "admin" {
			t.Fatalf("unexpected user: %+v", user)
		}
		if input.Name != "支付宝" || input.EPayType != "alipay" || input.Sort != 10 || !input.Enabled {
			t.Fatalf("unexpected input: %+v", input)
		}
		return entity.PaymentMethod{ID: 11, Name: input.Name, EPayType: input.EPayType, Enabled: input.Enabled, Sort: input.Sort}, nil
	}

	payload := []byte(`{"name":"支付宝","iconSvg":"<svg></svg>","epayType":"alipay","enabled":true,"sort":10,"remark":"常用"}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/payment-methods", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 9, Role: "admin", Status: "enabled"})

	CreatePaymentMethod(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var method entity.PaymentMethod
	if err := json.Unmarshal(recorder.Body.Bytes(), &method); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if method.ID != 11 || method.Name != "支付宝" {
		t.Fatalf("unexpected created payment method: %+v", method)
	}
}

func TestCreatePaymentMethodRejectsUnsafeIcon(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := createPaymentMethod
	defer func() { createPaymentMethod = original }()
	createPaymentMethod = func(user entity.User, input service.CreatePaymentMethodInput) (entity.PaymentMethod, error) {
		return entity.PaymentMethod{}, service.ErrPaymentMethodIconUnsafe
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/payment-methods", bytes.NewBufferString(`{"name":"支付宝","epayType":"alipay","iconSvg":"<svg><script>alert(1)</script></svg>"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 9, Role: "admin", Status: "enabled"})

	CreatePaymentMethod(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "SVG") {
		t.Fatalf("expected unsafe SVG message, got %s", recorder.Body.String())
	}
}

func TestUpdatePaymentMethodHandlesNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := updatePaymentMethod
	defer func() {
		updatePaymentMethod = original
	}()

	updatePaymentMethod = func(user entity.User, paymentMethodID int32, input service.UpdatePaymentMethodInput) (entity.PaymentMethod, error) {
		return entity.PaymentMethod{}, service.ErrPaymentMethodNotFound
	}

	payload := []byte(`{"name":"微信支付","epayType":"wxpay","enabled":true,"sort":20}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/payment-methods/3", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "id", Value: "3"}}
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	UpdatePaymentMethod(ctx)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestDeletePaymentMethodDeletesItemForAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := deletePaymentMethod
	defer func() {
		deletePaymentMethod = original
	}()

	called := false
	deletePaymentMethod = func(user entity.User, paymentMethodID int32) error {
		called = true
		if user.ID != 1 || user.Role != "admin" {
			t.Fatalf("unexpected user: %+v", user)
		}
		if paymentMethodID != 13 {
			t.Fatalf("expected payment method id 13, got %d", paymentMethodID)
		}
		return nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/adapter-service/payment-methods/13", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "13"}}
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	DeletePaymentMethod(ctx)

	if !called {
		t.Fatal("expected delete seam to be called")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestPaymentMethodListReturnsServerErrorWhenLoadFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := listPaymentMethods
	defer func() {
		listPaymentMethods = original
	}()

	listPaymentMethods = func(user entity.User) ([]entity.PaymentMethod, error) {
		return nil, errors.New("boom")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/payment-methods", nil)
	ctx.Set("user", &entity.User{ID: 10, Role: "admin", Status: "enabled"})

	PaymentMethodList(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}
