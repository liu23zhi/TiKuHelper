package util

import (
	"strings"
	"testing"
)

// TestSQLiteConcurrentDSNAddsImmediateAndBusyTimeout
// 并发测试用的 DSN 必须带 _txlock=immediate 与 busy_timeout。
//
// 这两条不是「性能优化」，而是并发用例能不能测到业务语义的前提：
// 默认 deferred 模式下先读后写的事务会直接 SQLITE_BUSY_SNAPSHOT，
// 而 busy_timeout 对快照升级无效 —— 落败方会全部被锁挡下，
// 断言照样绿，但覆盖的是错误分支。
func TestSQLiteConcurrentDSNAddsImmediateAndBusyTimeout(t *testing.T) {
	got := SQLiteConcurrentDSN("file:test?mode=memory&cache=shared")
	if !strings.Contains(got, "_txlock=immediate") {
		t.Fatalf("missing _txlock=immediate: %s", got)
	}
	if !strings.Contains(got, "_pragma=busy_timeout(5000)") {
		t.Fatalf("missing busy_timeout: %s", got)
	}
	// 内存库不能加 WAL：Concurrent 版本刻意不带 journal_mode。
	if strings.Contains(got, "journal_mode") {
		t.Fatalf("concurrent DSN must not set journal_mode (memory dbs cannot use WAL): %s", got)
	}
	if !strings.HasPrefix(got, "file:test?mode=memory&cache=shared") {
		t.Fatalf("original DSN must be preserved as a prefix: %s", got)
	}
}

// TestSQLiteConcurrentDSNIsIdempotent 已经带参数时不重复追加。
func TestSQLiteConcurrentDSNIsIdempotent(t *testing.T) {
	once := SQLiteConcurrentDSN("file:test?mode=memory&cache=shared")
	twice := SQLiteConcurrentDSN(once)
	if once != twice {
		t.Fatalf("not idempotent: once=%s twice=%s", once, twice)
	}
	if strings.Count(twice, "_txlock=") != 1 {
		t.Fatalf("_txlock duplicated: %s", twice)
	}
	if strings.Count(twice, "busy_timeout") != 1 {
		t.Fatalf("busy_timeout duplicated: %s", twice)
	}
}

// TestSQLiteConcurrentDSNUsesCorrectSeparator 无 ? 的 DSN 用 ? 起头，有 ? 的用 &。
func TestSQLiteConcurrentDSNUsesCorrectSeparator(t *testing.T) {
	bare := SQLiteConcurrentDSN("app.db")
	if !strings.HasPrefix(bare, "app.db?") {
		t.Fatalf("bare DSN must start params with ?: %s", bare)
	}
	withQuery := SQLiteConcurrentDSN("app.db?cache=shared")
	if !strings.HasPrefix(withQuery, "app.db?cache=shared&") {
		t.Fatalf("DSN with query must use & separator: %s", withQuery)
	}
}

// TestSQLiteProductionDSNKeepsWALForFilesAndSkipsMemory 生产版对文件库加 WAL，内存库原样返回。
func TestSQLiteProductionDSNKeepsWALForFilesAndSkipsMemory(t *testing.T) {
	fileDSN := SQLiteProductionDSN("app.db")
	for _, want := range []string{"_txlock=immediate", "_pragma=busy_timeout(5000)", "_pragma=journal_mode(WAL)", "_pragma=synchronous(NORMAL)"} {
		if !strings.Contains(fileDSN, want) {
			t.Fatalf("production DSN missing %s: %s", want, fileDSN)
		}
	}
	memory := "file:test?mode=memory&cache=shared"
	if got := SQLiteProductionDSN(memory); got != memory {
		t.Fatalf("memory DSN must be returned unchanged, got %s", got)
	}
	if got := SQLiteProductionDSN("file::memory:?cache=shared"); got != "file::memory:?cache=shared" {
		t.Fatalf(":memory: DSN must be returned unchanged, got %s", got)
	}
}

// TestSQLiteProductionDSNIsIdempotent 生产版同样幂等。
func TestSQLiteProductionDSNIsIdempotent(t *testing.T) {
	once := SQLiteProductionDSN("app.db")
	if twice := SQLiteProductionDSN(once); once != twice {
		t.Fatalf("not idempotent: once=%s twice=%s", once, twice)
	}
}

// TestSQLiteConcurrentDSNAndProductionShareTheSamePragmaConstants
// 两个入口必须共用同一份常量 —— 这是「生产与测试不漂移」的结构性保证。
func TestSQLiteConcurrentDSNAndProductionShareTheSamePragmaConstants(t *testing.T) {
	concurrent := SQLiteConcurrentDSN("app.db")
	production := SQLiteProductionDSN("app.db")
	for _, shared := range []string{SQLiteTxLockImmediate, SQLiteBusyTimeoutPragma} {
		if !strings.Contains(concurrent, shared) {
			t.Fatalf("concurrent DSN missing shared pragma %q: %s", shared, concurrent)
		}
		if !strings.Contains(production, shared) {
			t.Fatalf("production DSN missing shared pragma %q: %s", shared, production)
		}
	}
	// busy_timeout 的数值也必须来自同一个常量，不能两处各写一个数。
	if !strings.Contains(SQLiteBusyTimeoutPragma, "5000") {
		t.Fatalf("busy timeout pragma changed without updating SQLiteBusyTimeoutMillis: %s", SQLiteBusyTimeoutPragma)
	}
	if SQLiteBusyTimeoutMillis != 5000 {
		t.Fatalf("SQLiteBusyTimeoutMillis = %d, want 5000 (keep in sync with SQLiteBusyTimeoutPragma)", SQLiteBusyTimeoutMillis)
	}
}
