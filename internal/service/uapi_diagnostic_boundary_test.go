package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
)

func TestT12ScopedLocationFailureKeepsTypedErrorButNotPrivateDiagnostics(t *testing.T) {
	const private = "synthetic-provider-private-value"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", private)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"message":%q}`, private+r.URL.RawQuery)
	}))
	defer s.Close()
	client := uapi.NewClient("uapi-local-fixture").SetIPInfoURL(s.URL).SetPhoneInfoURL(s.URL).SetMaxRetries(0)
	var sink bytes.Buffer
	old := gin.DefaultWriter
	gin.DefaultWriter = &sink
	defer func() { gin.DefaultWriter = old }()
	for _, input := range []struct{ kind, key string }{
		{entity.LocationKindIP, "192.0.2.61"}, {entity.LocationKindPhone, "13800138000"},
	} {
		db := newLocationTestDB(t)
		rows, err := queryLocationRows(db, client, input.kind, []string{input.key}, "", true)
		var typed *uapi.APIError
		if len(rows) != 0 || !errors.Is(err, uapi.ErrForbidden) || !errors.As(err, &typed) ||
			typed.HTTPStatus != http.StatusForbidden || typed.RequestID != private {
			t.Fatalf("typed return changed: rows=%d err=%v", len(rows), err)
		}
		if strings.Contains(sink.String(), input.key) {
			t.Fatal("diagnostic contains the original IP/phone")
		}
	}
	text := sink.String()
	if strings.Contains(text, private) {
		t.Fatal("provider's raw message or ID escaped into a diagnostic")
	}
	hash := regexp.MustCompile(`"upstream_id_hash":"([a-f0-9]{64})"`).FindStringSubmatch(text)
	if len(hash) != 2 || !strings.Contains(text, "error_class=forbidden http_status=403 upstream_id_hash="+hash[1]) {
		t.Fatal("ordinary diagnostic cannot be correlated to the sanitized SDK event")
	}
}

func TestT12LegacySensitiveCacheFailureIsNotReplayed(t *testing.T) {
	const original = "uapi: HTTP 503 supplier-message-fixture (request_id=synthetic-raw-ID)"
	const query = "private-check-text"
	db := newSensitiveWordCacheDB(t)
	ensureSensitiveLogTable(t, db)
	if err := db.Exec("INSERT INTO logs (id,create_time) VALUES (77,?)", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	key := hashSensitiveWordText(query)
	if err := db.Create(&entity.SensitiveWordCache{
		TextHash: key, Error: original, AttemptCount: sensitiveWordCacheMaxAttempts,
	}).Error; err != nil {
		t.Fatal(err)
	}
	detector, calls := countingDetector(t, uapi.ProfanityCheckResponse{}, nil)
	outcome := RunSensitiveWordCheckWithDB(context.Background(), db, detector, query, "")
	if calls() != 0 || outcome.Flag || outcome.Results[0].Status != "error" {
		t.Fatalf("cached failure changed business decisions: calls=%d outcome=%+v", calls(), outcome)
	}
	if err := SaveSensitiveWordOutcomeWithDB(db, 77, outcome, time.Now()); err != nil {
		t.Fatal(err)
	}
	var logged struct{ Diagnostic, Words string }
	if err := db.Raw("SELECT sensitive_check_error AS diagnostic,sensitive_words AS words FROM logs WHERE id=77").Scan(&logged).Error; err != nil {
		t.Fatal(err)
	}
	if logged.Diagnostic != "question: upstream_5xx HTTP 503" || strings.Contains(logged.Words, "supplier-message-fixture") ||
		strings.Contains(logged.Words, "synthetic-raw-ID") || strings.Contains(logged.Words, query) {
		t.Fatalf("legacy failure replayed into a diagnostic: %+v", logged)
	}
	var cache entity.SensitiveWordCache
	if err := db.Take(&cache, "text_hash = ?", key).Error; err != nil || cache.Error != original {
		t.Fatal("historic authorized cache content was rewritten")
	}
}
