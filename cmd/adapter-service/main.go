package main

import (
	"embed"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/api"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/timer"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

//go:embed dist
var buildFS embed.FS

//go:embed dist/index.html
var indexPage []byte

func main() {
	mg, err := manager.CreateManager()
	if err != nil {
		logger.FatalLog(err)
	}
	defer func(mg *manager.Manager) {
		err := mg.CloseManager()
		if err != nil {
			logger.FatalLog(err)
		}
	}(mg)

	server := gin.Default()
	trustedProxies := []string(nil)
	if configured := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES")); configured != "" {
		for _, item := range strings.Split(configured, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				trustedProxies = append(trustedProxies, trimmed)
			}
		}
	}
	if err := server.SetTrustedProxies(trustedProxies); err != nil {
		logger.FatalLog(err)
	}
	api.SetAPIRouter(server)
	api.SetWebRouter(buildFS, indexPage, server)
	timer.StartTimer(mg.GetDB(), mg.GetEs(), mg.GetConfig().Elasticsearch.Addresses)
	httpServer := &http.Server{
		Addr:              "0.0.0.0:8060",
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      210 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	err = httpServer.ListenAndServe()
	if err != nil {
		logger.FatalLog(err)
	}
}
