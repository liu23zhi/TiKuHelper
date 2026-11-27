package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
)

func TestT12ReversedIPRangeWarningDoesNotPrintAddresses(t *testing.T) {
	const sample = "192.0.2.25"
	const rangeStart = "192.0.2.99"
	const rangeEnd = "192.0.2.5"
	const region = "fixture-region"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"ip": sample, "beginip": rangeStart, "endip": rangeEnd, "region": region,
		})
	}))
	defer server.Close()
	client := uapi.NewClient("uapi-fixture-local").SetIPInfoURL(server.URL).SetMaxRetries(0)
	db := newLocationTestDB(t)
	if err := db.AutoMigrate(&entity.IPSegment{}); err != nil {
		t.Fatal(err)
	}

	var sink bytes.Buffer
	out, errors := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &sink, &sink
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = out, errors })
	for _, step := range []struct {
		name    string
		refresh bool
	}{
		{name: "live", refresh: true},
		{name: "cache"},
	} {
		t.Run(step.name, func(t *testing.T) {
			sink.Reset()
			rows, err := queryLocationRows(db, client, entity.LocationKindIP, []string{sample}, "", step.refresh)
			if err != nil || len(rows) != 1 || rows[0].Address != region || calls.Load() != 1 {
				t.Fatalf("location result or reuse changed: rows=%d err=%t calls=%d", len(rows), err != nil, calls.Load())
			}
			message := sink.String()
			if strings.Contains(message, sample) || strings.Contains(message, rangeStart) || strings.Contains(message, rangeEnd) {
				t.Error("reversed range warning exposed an address")
			}
			if !strings.Contains(message, "reason=range_bounds_reversed") {
				t.Error("missing diagnostic reason for the rejected range")
			}
		})
	}
	var segments int64
	if err := db.Model(&entity.IPSegment{}).Count(&segments).Error; err != nil {
		t.Fatal(err)
	}
	if segments != 0 {
		t.Fatalf("reversed range was written: segments=%d", segments)
	}
}
