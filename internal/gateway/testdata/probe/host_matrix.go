// The deployment smoke can only address its own private numeric loopback interface.
// Synthetic Host values are HTTP input; no DNS lookup or script execution occurs.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime"
	"net/http"
	"os"
	"time"
)

func hostMatrix() {
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	type observation struct {
		Host, Method, Path, Type, SHA256 string
		Status, Bytes                    int
		SPA, JSON                        bool
	}
	var rows []observation
	for _, base := range []string{"tiku.example.test", "a.example.test", "unknown.invalid"} {
		for _, step := range []struct{ method, path string }{{"GET", "/"}, {"GET", "/api/public/register-status"}, {"GET", "/api/Wechat_qc_code"}, {"HEAD", "/api/Wechat_qc_code"}} {
			req, err := http.NewRequest(step.method, "http://127.0.0.1:8060"+step.path, nil)
			must(err)
			req.Host = base + ":8680"
			resp, err := client.Do(req)
			must(err)
			b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			must(err)
			must(resp.Body.Close())
			typ, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			rows = append(rows, observation{req.Host, step.method, step.path, typ, fmt.Sprintf("%x", sha256.Sum256(b)), resp.StatusCode, len(b), bytes.Contains(b, []byte(`id="app"`)), json.Valid(b)})
		}
	}
	must(json.NewEncoder(os.Stdout).Encode(rows))
}

func qrFixture() { must(jpeg.Encode(os.Stdout, image.NewGray(image.Rect(0, 0, 2, 2)), nil)) }
