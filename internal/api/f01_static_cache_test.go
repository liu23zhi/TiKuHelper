package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	f01static "github.com/itihey/tikuAdapter/internal/api/testdata/f01static"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

type f01CacheCheck struct {
	Label  string `json:"label"`
	Passed bool   `json:"passed"`
}
type f01CacheCase struct {
	Name   string          `json:"name"`
	Checks []f01CacheCheck `json:"checks"`
}
type f01CacheLedger struct {
	Cases         []f01CacheCase `json:"cases"`
	Requests      int            `json:"requests"`
	ExternalIO    int            `json:"externalIO"`
	DatabaseIO    int            `json:"databaseIO"`
	HTTPListeners int            `json:"httpListeners"`
}

// Actual SetWebRouter+synthetic embedded files only; no listener/DB/Provider.
func TestF01HashedStaticCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	index, err := f01static.Files.ReadFile("dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	SetWebRouter(f01static.Files, index, router)
	ledger := f01CacheLedger{}
	defer func() {
		raw, err := json.Marshal(ledger)
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Printf("F01_STATIC_CACHE_RESULT %s\n", raw)
		if out := os.Getenv("F01_STATIC_CACHE_LEDGER"); out != "" {
			if err := os.WriteFile(out, append(raw, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
	}()
	run := func(name string, fn func(func(string, bool), func(string, string, string) *httptest.ResponseRecorder)) {
		t.Run(name, func(t *testing.T) {
			cur := f01CacheCase{Name: name}
			check := func(label string, pass bool) {
				cur.Checks = append(cur.Checks, f01CacheCheck{label, pass})
				if !pass {
					t.Errorf("%s", label)
				}
			}
			request := func(method, target, validator string) *httptest.ResponseRecorder {
				ledger.Requests++
				req := httptest.NewRequest(method, target, nil)
				if validator != "" {
					req.Header.Set("If-None-Match", validator)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				return w
			}
			fn(check, request)
			ledger.Cases = append(ledger.Cases, cur)
		})
	}
	immutable := "public, max-age=31536000, immutable"
	for _, target := range []string{"/js/app.a1b2c3d4.js", "/css/app.11223344.css"} {
		body, err := f01static.Files.ReadFile("dist" + target)
		if err != nil {
			t.Fatal(err)
		}
		tag := fmt.Sprintf("\"%x\"", sha256.Sum256(body))
		run("GET_HEAD"+target, func(check func(string, bool), request func(string, string, string) *httptest.ResponseRecorder) {
			get := request("GET", target, "")
			check("GET exact embedded bytes200", get.Code == 200 && get.Body.String() == string(body))
			check("GET immutable public", get.Header().Get("Cache-Control") == immutable)
			check("ETag full content SHA256", get.Header().Get("ETag") == tag)
			check("no gzip added", get.Header().Get("Content-Encoding") == "")
			head := request("HEAD", target, "")
			check("HEAD bodyless200 exact length", head.Code == 200 && head.Body.Len() == 0 && head.Header().Get("Content-Length") == strconv.Itoa(len(body)))
			check("HEAD same ETag/policy", head.Header().Get("ETag") == tag && head.Header().Get("Cache-Control") == immutable)
		})
		run("validators"+target, func(check func(string, bool), request func(string, string, string) *httptest.ResponseRecorder) {
			for _, validator := range []string{tag, "W/" + tag, "\"unmatched\", W/" + tag, "*"} {
				for _, method := range []string{"GET", "HEAD"} {
					w := request(method, target, validator)
					check(method+" matching validator bodyless304", w.Code == 304 && w.Body.Len() == 0)
					check(method+"304 same ETag/policy", w.Header().Get("ETag") == tag && w.Header().Get("Cache-Control") == immutable)
				}
			}
			miss := request("GET", target, "\"different\"")
			check("nonmatching validator exact200", miss.Code == 200 && miss.Body.String() == string(body))
		})
	}
	run("range_and_IfRange", func(check func(string, bool), _ func(string, string, string) *httptest.ResponseRecorder) {
		body, _ := f01static.Files.ReadFile("dist/js/app.a1b2c3d4.js")
		tag := fmt.Sprintf("\"%x\"", sha256.Sum256(body))
		for _, item := range []struct {
			validator string
			status    int
			partial   bool
		}{{tag, 206, true}, {"W/" + tag, 200, false}, {"\"different\"", 200, false}} {
			ledger.Requests++
			req := httptest.NewRequest("GET", "/js/app.a1b2c3d4.js", nil)
			req.Header.Set("Range", "bytes=0-6")
			req.Header.Set("If-Range", item.validator)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			wantBody := string(body)
			if item.partial {
				wantBody = string(body[:7])
			}
			check("If-Range strong match only permits exact206", w.Code == item.status && w.Body.String() == wantBody)
		}
	})
	run("nonhash_missing_path_directory", func(check func(string, bool), request func(string, string, string) *httptest.ResponseRecorder) {
		for _, target := range []string{"/", "/login", "/index.html", "/theme-init.js", "/version.json", "/js/legacy.js", "/js/app.abcdefgh.js", "/js/page.00112233.html", "/assets/report.99aabbcc.js", "/js/missing.12345678.js", "/css/missing.12345678.css", "/js/directory.aabbccdd.js"} {
			w := request("GET", target, "")
			check(target+" not immutable/ETag/304", !strings.Contains(w.Header().Get("Cache-Control"), "immutable") && w.Header().Get("ETag") == "" && w.Code != 304)
		}
		for _, target := range []string{"/js/../js/app.a1b2c3d4.js", "/js/%61pp.a1b2c3d4.js"} {
			w := request("GET", target, "")
			check("noncanonical path no immutable/ETag", !strings.Contains(w.Header().Get("Cache-Control"), "immutable") && w.Header().Get("ETag") == "")
		}
	})
	run("nonGET_API_SPA", func(check func(string, bool), request func(string, string, string) *httptest.ResponseRecorder) {
		body, _ := f01static.Files.ReadFile("dist/js/app.a1b2c3d4.js")
		for _, method := range []string{"POST", "PUT"} {
			w := request(method, "/js/app.a1b2c3d4.js", "\"unmatched\"")
			check(method+" original FileServer bytes", w.Code == 200 && w.Body.String() == string(body))
			check(method+" no new cache policy", w.Header().Get("Cache-Control") == "" && w.Header().Get("ETag") == "")
		}
		for _, method := range []string{"GET", "POST", "HEAD"} {
			w := request(method, "/api/missing.12345678.js", "*")
			check(method+" API JSON404 uncached", w.Code == 404 && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") && w.Header().Get("Cache-Control") == "" && w.Header().Get("ETag") == "")
		}
		w := request("GET", "/adapter/tiku", "*")
		check("SPA fallback unchanged uncached", w.Code == 200 && w.Body.String() == string(index) && w.Header().Get("Cache-Control") == "" && w.Header().Get("ETag") == "")
	})
}
