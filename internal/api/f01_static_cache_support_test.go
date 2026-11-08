package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	f01static "github.com/itihey/tikuAdapter/internal/api/testdata/f01static"
)

type f01SupportFS struct {
	base      http.FileSystem
	readBytes atomic.Int64
}
type f01SupportFile struct {
	http.File
	owner *f01SupportFS
}

func (f *f01SupportFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	f.owner.readBytes.Add(int64(n))
	return n, err
}
func (f *f01SupportFS) Open(name string) (http.File, error) {
	file, err := f.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &f01SupportFile{File: file, owner: f}, nil
}
func (f *f01SupportFS) Exists(_ string, name string) bool {
	file, err := f.Open(name)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}
func f01SupportNewFS(body string) *f01SupportFS {
	return &f01SupportFS{base: http.FS(fstest.MapFS{
		"js/app.a1b2c3d4.js":   {Data: []byte(body)},
		"css/app.11223344.css": {Data: []byte("body{color:#123456}")},
		"theme-init.js":        {Data: []byte("must not be hashed")},
	})}
}
func f01SupportRouter(files *f01SupportFS) *gin.Engine {
	r := gin.New()
	r.Use(serveImmutableWebAssets(files))
	r.NoRoute(func(c *gin.Context) { c.Status(404) })
	return r
}

// These additions do not change the original71 checks. Counting uses the real
// production factory with two isolated filesystems; request/error tests retain
// stdlib conditional processing, not a substitute validator implementation.
func TestF01StaticCacheSupport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("GODEBUG", "httpservecontentkeepheaders=1")
	checks := []f01CacheCheck{}
	check := func(t *testing.T, label string, pass bool) {
		t.Helper()
		checks = append(checks, f01CacheCheck{label, pass})
		if !pass {
			t.Error(label)
		}
	}
	var observedConstruction, observedAfter int64
	defer func() {
		raw, err := json.Marshal(struct {
			Checks                    []f01CacheCheck `json:"checks"`
			ConstructionReadBytes     int64           `json:"constructionReadBytes"`
			AfterConditionalReadBytes int64           `json:"afterConditionalReadBytes"`
		}{checks, observedConstruction, observedAfter})
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Printf("F01_STATIC_CACHE_SUPPORT_RESULT %s\n", raw)
		if out := os.Getenv("F01_STATIC_CACHE_SUPPORT_LEDGER"); out != "" {
			if err := os.WriteFile(out, append(raw, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
	}()
	t.Run("error_status_headers", func(t *testing.T) {
		index, _ := f01static.Files.ReadFile("dist/index.html")
		r := gin.New()
		SetWebRouter(f01static.Files, index, r)
		for _, item := range []struct {
			name, key, value string
			status           int
		}{
			{"IfMatch412", "If-Match", "\"different\"", 412},
			{"Range416", "Range", "bytes=999999-", 416},
		} {
			req := httptest.NewRequest("GET", "/js/app.a1b2c3d4.js", nil)
			req.Header.Set(item.key, item.value)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			check(t, item.name+" preserves stdlib status", w.Code == item.status)
			check(t, item.name+" error never immutable/ETag even keepheaders", w.Header().Get("Cache-Control") == "" && w.Header().Get("ETag") == "")
		}
	})
	t.Run("construct_once_two_filesystems", func(t *testing.T) {
		aBody, bBody := "synthetic A bytes", "different synthetic B bytes"
		a, b := f01SupportNewFS(aBody), f01SupportNewFS(bBody)
		ar, br := f01SupportRouter(a), f01SupportRouter(b)
		wantReadA := int64(len(aBody) + len("body{color:#123456}"))
		observedConstruction = a.readBytes.Load()
		check(t, "construction hashes exactly eligible regular JS/CSS bytes once", a.readBytes.Load() == wantReadA)
		check(t, "second independent FS has its own exact construction", b.readBytes.Load() == int64(len(bBody)+len("body{color:#123456}")))
		aTag, bTag := fmt.Sprintf("\"%x\"", sha256.Sum256([]byte(aBody))), fmt.Sprintf("\"%x\"", sha256.Sum256([]byte(bBody)))
		for _, pair := range []struct {
			r   *gin.Engine
			tag string
		}{{ar, aTag}, {br, bTag}, {ar, aTag}, {br, bTag}} {
			req := httptest.NewRequest("HEAD", "/js/app.a1b2c3d4.js", nil)
			req.Header.Set("If-None-Match", pair.tag)
			w := httptest.NewRecorder()
			pair.r.ServeHTTP(w, req)
			check(t, "FS local bodySHA identity and conditional304", w.Code == 304 && w.Header().Get("ETag") == pair.tag && w.Body.Len() == 0)
		}
		observedAfter = a.readBytes.Load()
		check(t, "repeated conditional requests do not rehash/read body", observedAfter == observedConstruction && b.readBytes.Load() == int64(len(bBody)+len("body{color:#123456}")))
		before := a.readBytes.Load()
		var failures atomic.Int64
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req := httptest.NewRequest("HEAD", "/js/app.a1b2c3d4.js", nil)
				req.Header.Set("If-None-Match", aTag)
				w := httptest.NewRecorder()
				ar.ServeHTTP(w, req)
				if w.Code != 304 || w.Header().Get("ETag") != aTag {
					failures.Add(1)
				}
			}()
		}
		wg.Wait()
		check(t, "concurrent immutable index reads preserve each response", failures.Load() == 0)
		check(t, "concurrent conditional requests add zero hash/body reads", a.readBytes.Load() == before)
	})
}
