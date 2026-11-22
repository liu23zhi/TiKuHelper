package api

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/controller"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDeploymentQRCodeOnlyServesFixedRegularJPEG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "q.jpg")
	r := gin.New()
	h := deploymentQRCode(path)
	r.GET("/api/Wechat_qc_code", h)
	r.HEAD("/api/Wechat_qc_code", h)
	call := func(method string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, "/api/Wechat_qc_code", nil))
		return w
	}
	if w := call("GET"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if w := call("GET"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a JPEG"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := call("GET"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if err := os.WriteFile(path, []byte{0xff, 0xd8, 0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if w := call("GET"); w.Code != 404 {
		t.Fatal("truncated JPEG was served")
	}
	var img bytes.Buffer
	if err := jpeg.Encode(&img, image.NewGray(image.Rect(0, 0, 3, 3)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, img.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if w := call("GET"); w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), img.Bytes()) {
		t.Fatalf("valid image: status=%d bytes=%d", w.Code, w.Body.Len())
	}
	if w := call("HEAD"); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("HEAD %d %d", w.Code, w.Body.Len())
	}
	t.Run("symlink", func(t *testing.T) {
		alias := filepath.Join(dir, "alias.jpg")
		if err := os.Symlink(path, alias); err != nil {
			t.Skip("no symlink permission")
		}
		if f, err := os.Lstat(alias); err != nil || f.Mode().IsRegular() {
			t.Fatal("fixture invalid")
		}
		rr := gin.New()
		rr.GET("/q", deploymentQRCode(alias))
		w := httptest.NewRecorder()
		rr.ServeHTTP(w, httptest.NewRequest("GET", "/q", nil))
		if w.Code != 404 {
			t.Fatal(w.Code)
		}
	})
}

func TestDeploymentQRIsGatedByActualPublicAuthorityMiddleware(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:qr-authority?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sql.Close() })
	if err = db.AutoMigrate(&entity.SystemSetting{}, &entity.UserScript{}, &entity.ScriptAddressReservation{}); err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&entity.UserScript{ID: 1, ShortCode: "assigned-fixture", ShortEntryAddress: "https://a.example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = service.UpdateScriptDomainSettingsWithDB(db, service.ScriptDomainSettings{Domains: []string{"*.example.test"}}); err != nil {
		t.Fatal(err)
	}
	if err = service.ConfigureSiteHosts([]string{"tiku.example.test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.ConfigureSiteHosts(nil) })
	r := gin.New()
	r.Use(controller.ScriptHostBoundary(db))
	p := filepath.Join(t.TempDir(), "qr.jpg")
	var img bytes.Buffer
	if err = jpeg.Encode(&img, image.NewGray(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, img.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	registerDeploymentQRCode(r, p)
	for _, host := range []string{"TIKU.Example.Test.:9043", "a.example.test:9043", "other.invalid:9043", "localhost:8680", "127.0.0.1:8680", "[::1]:8680"} {
		for _, method := range []string{"GET", "HEAD"} {
			req := httptest.NewRequest(method, "/api/Wechat_qc_code", nil)
			req.Host = host
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			want := 200
			if host == "a.example.test:9043" {
				want = 404
			}
			if w.Code != want || ((w.Header().Get("Content-Type") == "image/jpeg") != (want == 200)) || (method == "HEAD" && w.Body.Len() != 0) || (method == "GET" && want == 200 && !bytes.Equal(img.Bytes(), w.Body.Bytes())) {
				t.Fatalf("%s %s: code=%d bytes=%d", method, host, w.Code, w.Body.Len())
			}
		}
	}
}
