package controller

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPaymentIconAPIRejects400BeforePersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&entity.PaymentMethod{}); err != nil {
		t.Fatal(err)
	}
	legacy := entity.PaymentMethod{Name: "legacy", IconSVG: " <svg onload=\"INERT_ONLY\"/> ", EPayType: "fixture", Enabled: true}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&legacy, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	oldCreate, oldUpdate := createPaymentMethod, updatePaymentMethod
	defer func() { createPaymentMethod = oldCreate; updatePaymentMethod = oldUpdate }()
	createPaymentMethod = func(_ entity.User, in service.CreatePaymentMethodInput) (entity.PaymentMethod, error) {
		return service.CreatePaymentMethodWithDB(db, in)
	}
	updatePaymentMethod = func(_ entity.User, id int32, in service.UpdatePaymentMethodInput) (entity.PaymentMethod, error) {
		return service.UpdatePaymentMethodWithDB(db, id, in)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		body, _ := json.Marshal(map[string]any{"name": "changed", "epayType": "fixture", "iconSvg": legacy.IconSVG, "enabled": false})
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Request = httptest.NewRequest(method, "/adapter-service/payment-methods/1", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
		c.Params = gin.Params{{Key: "id", Value: "1"}}
		if method == http.MethodPost {
			CreatePaymentMethod(c)
		} else {
			UpdatePaymentMethod(c)
		}
		if r.Code != 400 {
			t.Fatalf("%s status=%d body=%s", method, r.Code, r.Body.String())
		}
	}
	var rows []entity.PaymentMethod
	if err := db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0] != legacy {
		t.Fatalf("unexpected writes: %+v %v", rows, err)
	}
}

func TestPaymentIconAPIAnonymousAndNonAdminNeverWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err = db.AutoMigrate(&entity.PaymentMethod{}); err != nil {
		t.Fatal(err)
	}
	original := entity.PaymentMethod{Name: "legacy", IconSVG: " <svg onload=\"INERT_ONLY\"/> ", EPayType: "fixture", Enabled: true}
	if err = db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&original, original.ID).Error; err != nil {
		t.Fatal(err)
	}
	oldCreate, oldUpdate, oldDelete := createPaymentMethod, updatePaymentMethod, deletePaymentMethod
	defer func() {
		createPaymentMethod = oldCreate
		updatePaymentMethod = oldUpdate
		deletePaymentMethod = oldDelete
	}()
	writes := 0
	createPaymentMethod = func(_ entity.User, in service.CreatePaymentMethodInput) (entity.PaymentMethod, error) {
		writes++
		return service.CreatePaymentMethodWithDB(db, in)
	}
	updatePaymentMethod = func(_ entity.User, id int32, in service.UpdatePaymentMethodInput) (entity.PaymentMethod, error) {
		writes++
		return service.UpdatePaymentMethodWithDB(db, id, in)
	}
	deletePaymentMethod = func(_ entity.User, id int32, _, _ string) error {
		writes++
		return service.DeletePaymentMethodWithDB(db, id)
	}
	for _, anonymous := range []bool{true, false} {
		for _, verb := range []string{"POST", "PUT", "DELETE"} {
			r := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(r)
			c.Request = httptest.NewRequest(verb, "/adapter-service/payment-methods/1", bytes.NewBufferString(`{"name":"changed","epayType":"fixture","iconSvg":"<svg/>"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Params = gin.Params{{Key: "id", Value: "1"}}
			want := 401
			if !anonymous {
				want = 403
				c.Set("user", &entity.User{ID: 2, Role: "user", Status: "enabled"})
			}
			switch verb {
			case "POST":
				CreatePaymentMethod(c)
			case "PUT":
				UpdatePaymentMethod(c)
			case "DELETE":
				DeletePaymentMethod(c)
			}
			if r.Code != want {
				t.Fatalf("anonymous=%v verb=%s status=%d want=%d body=%s", anonymous, verb, r.Code, want, r.Body.String())
			}
		}
	}
	var rows []entity.PaymentMethod
	if err = db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0] != original || writes != 0 {
		t.Fatalf("unauthorized writes=%d rows=%+v err=%v", writes, rows, err)
	}
}
