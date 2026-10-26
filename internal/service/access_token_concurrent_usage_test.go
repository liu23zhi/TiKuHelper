package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const (
	accessTokenUsageChildEnv = "TIKU_TEST_ACCESS_TOKEN_USAGE_CHILD"
	accessTokenUsageComplete = "ACCESS_TOKEN_METADATA_USAGE_PRESERVED"
)

// Keep the database work in an owned child: a driver that does not drain on
// cancellation must not leave SQL goroutines using a removed temporary file.
func TestUpdateAccessTokenWithDBPreservesConcurrentUsage(t *testing.T) {
	if os.Getenv(accessTokenUsageChildEnv) != "" {
		t.Fatal("parent must not run in child mode")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable,
		"-test.run=^TestUpdateAccessTokenWithDBPreservesConcurrentUsageChild$",
		"-test.v", "-test.timeout=16s")
	cmd.Env = append(os.Environ(), accessTokenUsageChildEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	t.Logf("child stdout:\n%s", stdout.Bytes())
	if stderr.Len() != 0 {
		t.Logf("child stderr:\n%s", stderr.Bytes())
	}
	if err != nil || ctx.Err() != nil || stderr.Len() != 0 {
		t.Fatalf("metadata usage child failed: run=%v context=%v", err, ctx.Err())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(accessTokenUsageComplete)) {
		t.Fatal("child did not reach the complete usage assertion")
	}
}

// This entry point is skipped in an ordinary suite; its parent exercises it.
func TestUpdateAccessTokenWithDBPreservesConcurrentUsageChild(t *testing.T) {
	if os.Getenv(accessTokenUsageChildEnv) != "1" {
		t.Skip("owned metadata-usage child; exercised by parent")
	}
	watchdog := time.AfterFunc(12*time.Second, func() {
		fmt.Fprintln(os.Stderr, "metadata usage child watchdog")
		os.Exit(24)
	})
	defer watchdog.Stop()
	a, b := newAccessTokenMetadataUsagePair(t)
	owner, initial := seedAccessTokenMetadataUsage(t, a, "enabled")
	ready, release := make(chan entity.AccessToken, 1), make(chan struct{})
	var firstRead atomic.Bool
	if err := a.Callback().Query().After("gorm:query").Register("access_token_metadata_loaded", func(tx *gorm.DB) {
		token, ok := tx.Statement.Dest.(*entity.AccessToken)
		if tx.Error == nil && ok && token.ID == initial.ID && firstRead.CompareAndSwap(false, true) {
			ready <- *token
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		token entity.AccessToken
		err   error
	}
	results, finished := make(chan result, 1), make(chan struct{})
	joined := false
	defer func() {
		if !joined {
			fmt.Fprintln(os.Stderr, "metadata update not joined; stopping child before database cleanup")
			os.Exit(25)
		}
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		token, err := UpdateAccessTokenWithDB(a.WithContext(ctx), owner, initial.ID,
			UpdateAccessTokenInput{Name: " token_b ", Status: "enabled", Remark: " metadata edit ", TotalQuotaLimit: 0.1})
		cancel()
		results <- result{token, err}
		close(finished)
	}()
	var loaded entity.AccessToken
	select {
	case loaded = <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("metadata load barrier not reached")
	}
	assertAccessTokenMetadataIdentity(t, loaded, initial)
	if loaded.Name != initial.Name || loaded.Status != initial.Status || loaded.Remark != initial.Remark ||
		loaded.TotalQuotaLimit != initial.TotalQuotaLimit || loaded.TotalAmountUsed != initial.TotalAmountUsed ||
		!loaded.UpdatedAt.Equal(initial.UpdatedAt) {
		t.Fatal("loaded snapshot differs from seeded token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	update := b.WithContext(ctx).Model(&entity.AccessToken{}).Where("id = ?", initial.ID).
		Select("TotalAmountUsed").Updates(&entity.AccessToken{TotalAmountUsed: 0.030003})
	cancel()
	if update.Error != nil || update.RowsAffected != 1 {
		t.Fatalf("independent usage write: rows=%d error=%v", update.RowsAffected, update.Error)
	}
	committed := loadAccessTokenMetadataUsage(t, b, initial.ID)
	assertAccessTokenMetadataIdentity(t, committed, initial)
	if committed.TotalAmountUsed != 0.030003 || committed.Name != initial.Name || committed.Status != initial.Status ||
		committed.Remark != initial.Remark || committed.TotalQuotaLimit != initial.TotalQuotaLimit ||
		committed.UpdatedAt.IsZero() || committed.UpdatedAt.Before(initial.UpdatedAt) {
		t.Fatalf("independent usage was not committed before metadata release: %+v", committed)
	}
	t.Logf("independent usage committed before metadata release: %.6f", committed.TotalAmountUsed)
	close(release)
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	var got result
	select {
	case got = <-results:
	case <-deadline.C:
		t.Fatal("metadata update did not return")
	}
	select {
	case <-finished:
	case <-deadline.C:
		t.Fatal("metadata update goroutine did not join")
	}
	joined = true
	final := loadAccessTokenMetadataUsage(t, b, initial.ID)
	t.Logf("metadata result: returned=%+v error=%v persisted=%+v", got.token, got.err, final)
	if got.err != nil {
		t.Fatal(got.err)
	}
	for _, token := range []entity.AccessToken{got.token, final} {
		assertAccessTokenMetadataIdentity(t, token, initial)
		if token.Name != "token_b" || token.Status != "enabled" || token.Remark != "metadata edit" || token.TotalQuotaLimit != 0.1 ||
			token.UpdatedAt.IsZero() || token.UpdatedAt.Before(initial.UpdatedAt) || token.UpdatedAt.Before(committed.UpdatedAt) {
			t.Fatalf("updated token metadata or timestamp mismatch: %+v", token)
		}
	}
	// The selected update must preserve the committed row, and its readback must
	// return the current usage rather than the original loaded snapshot.
	t.Logf("returned usage=%.6f persisted usage=%.6f", got.token.TotalAmountUsed, final.TotalAmountUsed)
	if final.TotalAmountUsed != 0.030003 {
		t.Fatalf("metadata update overwrote committed usage: stored=%.6f want=0.030003", final.TotalAmountUsed)
	}
	if got.token.TotalAmountUsed != 0.030003 {
		t.Fatalf("metadata readback returned stale usage: returned=%.6f want=0.030003", got.token.TotalAmountUsed)
	}
	t.Log(accessTokenUsageComplete)
}

func newAccessTokenMetadataUsagePair(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metadata-usage.db")
	a, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlA, err := a.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlA.Close(); err != nil {
			t.Errorf("close database A: %v", err)
		}
	})
	if err = a.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.Log{}); err != nil {
		t.Fatal(err)
	}
	b, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlB, err := b.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlB.Close(); err != nil {
			t.Errorf("close database B: %v", err)
		}
	})
	if sqlA == sqlB {
		t.Fatal("independent SQL handles required")
	}
	return a, b
}

func seedAccessTokenMetadataUsage(t *testing.T, db *gorm.DB, status string) (entity.User, entity.AccessToken) {
	t.Helper()
	owner := seedAccessTokenOwner(t, db, 2, "user")
	fixed := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	last := fixed.Add(-time.Hour)
	token := entity.AccessToken{UserID: owner.ID, Name: "token_a", TokenHash: hashAccessTokenValue("tk_synthetic_metadata_usage_only"),
		TokenPrefix: "tk_synthetic", TokenValue: "tk_synthetic_metadata_usage_only", Status: status, TotalQuotaLimit: 0.1,
		TotalAmountUsed: 0.020001, Remark: "before", LastUsedAt: &last, LastUsedIP: "192.0.2.17", CreatedAt: fixed, UpdatedAt: fixed}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	stored := loadAccessTokenMetadataUsage(t, db, token.ID)
	assertAccessTokenMetadataIdentity(t, stored, token)
	if stored.ID != 1 || stored.Name != "token_a" || stored.Status != status || stored.Remark != "before" ||
		stored.TotalQuotaLimit != 0.1 || stored.TotalAmountUsed != 0.020001 || !stored.UpdatedAt.Equal(fixed) {
		t.Fatalf("complete synthetic token seed mismatch: %+v", stored)
	}
	return owner, stored
}

func loadAccessTokenMetadataUsage(t *testing.T, db *gorm.DB, id int32) entity.AccessToken {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var token entity.AccessToken
	if err := db.WithContext(ctx).First(&token, id).Error; err != nil {
		t.Fatal(err)
	}
	return token
}

func assertAccessTokenMetadataIdentity(t *testing.T, got, want entity.AccessToken) {
	t.Helper()
	if got.ID != want.ID || got.UserID != want.UserID || got.TokenHash != want.TokenHash || got.TokenPrefix != want.TokenPrefix ||
		got.TokenValue != want.TokenValue || got.LastUsedAt == nil || want.LastUsedAt == nil || !got.LastUsedAt.Equal(*want.LastUsedAt) ||
		got.LastUsedIP != want.LastUsedIP || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("immutable token identity or last-usage metadata changed: got=%+v want=%+v", got, want)
	}
}
