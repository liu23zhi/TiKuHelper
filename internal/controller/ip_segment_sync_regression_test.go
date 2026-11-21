package controller

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestIPSegmentSyncUsesCompositeCacheKeyAndIgnoresPhoneRows(t *testing.T) {
	db := newIPSegmentControllerTestDB(t)
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatal(err)
	}
	rows := []entity.LocationCache{
		{Kind: "ip", LookupKey: "120.239.173.110", Address: "中国 广东", RawJSON: `{"ip":"120.239.173.110","beginip":"120.239.173.0","endip":"120.239.173.255","region":"中国 广东"}`, FetchedAt: time.Now()},
		{Kind: "phone", LookupKey: "13812345678", Address: "中国 广东", RawJSON: `{}`, FetchedAt: time.Now()},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/sync", func(c *gin.Context) { c.Set("user", &entity.User{ID: 1, Role: "admin"}) }, IPSegmentSyncFromCache)
	for round := 0; round < 2; round++ {
		res := ipSegmentRequest(router, http.MethodPost, "/sync", "")
		var body struct {
			Scanned  int `json:"scanned"`
			Inserted int `json:"inserted"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		want := 1
		if round == 1 {
			want = 0
		}
		if res.Code != 200 || body.Scanned != 1 || body.Inserted != want {
			t.Fatalf("round%d: %d %s", round, res.Code, res.Body.String())
		}
	}
}
