package controller

import (
	"testing"

	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestCloneAIFallbackResultIsolatesAttachmentDiagnostics(t *testing.T) {
	source := service.AIFallbackResult{Usage: searchpkg.AISearchUsage{Attachments: []searchpkg.AIAttachmentDiagnostic{{SourceIndex: 1, DownloadStatus: "success"}}}}
	cloned := cloneAIFallbackResult(source)
	cloned.Usage.Attachments[0].DownloadStatus = "error"
	if source.Usage.Attachments[0].DownloadStatus != "success" {
		t.Fatal("cache clone shares attachment diagnostics with its caller")
	}
}
