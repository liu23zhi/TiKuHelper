package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestReviewRebateAvailabilityMatchesExistingActivityAndUserView(t *testing.T) {
	db := newReviewRebateControllerTestDB(t)
	installReviewRebateTestDB(t, db)
	member := seedReviewRebateControllerUser(t, db, 699, "availability-member", "user")
	program, _ := seedReviewRebateControllerProgram(t, db)
	setReviewRebateControllerSwitch(t, db, false)
	var actor *entity.User
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/availability", auth, ReviewRebateAvailability)
	router.GET("/program", auth, ReviewRebateProgram)
	read := func(path string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest(http.MethodGet, path, nil))
		return out
	}
	if out := read("/availability"); out.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous availability: %d", out.Code)
	}
	actor = &member
	for _, enabled := range []bool{true, false, true} {
		if err := db.Model(&program).Update("enabled", enabled).Error; err != nil {
			t.Fatal(err)
		}
		// The real reported state: enabled activity plus stale legacy false.
		availability := read("/availability")
		if availability.Code != http.StatusOK || availability.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("availability: %d %s", availability.Code, availability.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(availability.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["enabled"] != enabled {
			t.Fatalf("availability leaks detail or disagrees with activity: %v", body)
		}
		detail := read("/program")
		if enabled && detail.Code != http.StatusOK {
			t.Fatalf("existing enabled activity blocked: %d %s", detail.Code, detail.Body.String())
		}
		if !enabled && detail.Code != http.StatusBadRequest {
			t.Fatalf("disabled activity open: %d", detail.Code)
		}
	}
	var legacy entity.SystemSetting
	if err := db.Where("key = ?", service.ReviewRebateEnabledSettingKey).Take(&legacy).Error; err != nil || legacy.Value != "false" {
		t.Fatalf("reads changed persisted legacy state: %+v %v", legacy, err)
	}
}
