package api

import (
	"image/jpeg"
	"io"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func registerDeploymentQRCode(r *gin.Engine, path string) {
	h := deploymentQRCode(path)
	r.GET("/api/Wechat_qc_code", h)
	r.HEAD("/api/Wechat_qc_code", h)
}

// The path is fixed by the deployment, never from the request. The shared host
// boundary protects this normal-site route just like the rest of the web UI.
func deploymentQRCode(path string) gin.HandlerFunc {
	return func(c *gin.Context) {
		miss := func() { c.AbortWithStatus(http.StatusNotFound) }
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() {
			miss()
			return
		}
		f, err := os.Open(path)
		if err != nil {
			miss()
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
			miss()
			return
		}
		// Validate JPEG headers/dimensions without decompressing pixels. A
		// three-byte JPEG signature alone is not a usable QR image.
		if _, err = jpeg.DecodeConfig(f); err != nil {
			miss()
			return
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			miss()
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Content-Type", "image/jpeg")
		http.ServeContent(c.Writer, c.Request, "Wechat_qc_code.jpg", info.ModTime(), f)
	}
}
