package util

import "strings"

// SQLiteConcurrencyPragmas 是 SQLite 连接必须携带的并发参数（不含 WAL）。
//
// 为什么单独抽出来（2026-11-09 复盘结论，见 task_doc/47-并发测试SQLite-DSN必须与生产一致.md）：
//
//	并发测试如果自己手写 DSN 字符串，就会与生产实现悄悄漂移 ——
//	实测 47 个含 go func 的测试文件里，除 sqliteProductionDSN 自身之外
//	没有任何一处使用生产 DSN。而这类漂移**不会让测试变红**：
//	并发用例通常只断言「只有一次成功」，而被锁挡下与被业务闸挡下
//	在断言上看起来一模一样，于是测试是绿的，覆盖的却是错误分支。
//
// 把参数定义放在中立叶子包（pkg/util 不 import internal），
// 生产侧（internal/registry）与测试侧（service/controller/registry）共用同一份常量，
// 从此「生产改了测试没跟着改」在结构上不可能发生。
const (
	// SQLiteTxLockImmediate 让写事务在 BEGIN 时就拿写锁。
	//
	// SQLite 默认 deferred 模式下，「先读后写」的事务在把读快照升级为写事务时
	// 会直接报 SQLITE_BUSY_SNAPSHOT，而 busy_timeout 对快照升级无效
	// （它只负责等待其他写者释放锁，管不了自己的读快照已经过期）。
	// 本仓几乎所有写事务都是先读后写，因此必须用 immediate。
	SQLiteTxLockImmediate = "_txlock=immediate"
	// SQLiteBusyTimeoutMillis 是等待其他写者释放锁的上限。
	SQLiteBusyTimeoutMillis = 5000
	// SQLiteBusyTimeoutPragma 是上面那个超时对应的 pragma 参数。
	SQLiteBusyTimeoutPragma = "_pragma=busy_timeout(5000)"
	// SQLiteJournalModePragma 开启 WAL。内存库不支持 WAL，故只用于文件库。
	SQLiteJournalModePragma = "_pragma=journal_mode(WAL)"
	// SQLiteSynchronousPragma 与 WAL 搭配，兼顾安全与写性能。
	SQLiteSynchronousPragma = "_pragma=synchronous(NORMAL)"
)

// SQLiteConcurrentDSN 给任意 SQLite DSN 补上并发必需参数（immediate + busy_timeout）。
//
// 与 SQLiteProductionDSN 的区别：**不改 journal_mode**，因此可以安全用于内存库
// （`file:xxx?mode=memory&cache=shared` 不支持 WAL）。测试库通常用内存库，
// 所以并发测试应当用这个函数，而不是直接抄生产 DSN 的整串。
//
// 幂等：已经带 _txlock / busy_timeout 的 DSN 原样返回。
func SQLiteConcurrentDSN(dsn string) string {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return trimmed
	}
	additions := make([]string, 0, 2)
	if !strings.Contains(trimmed, "_txlock=") {
		additions = append(additions, SQLiteTxLockImmediate)
	}
	if !strings.Contains(trimmed, "busy_timeout") {
		additions = append(additions, SQLiteBusyTimeoutPragma)
	}
	if len(additions) == 0 {
		return trimmed
	}
	return trimmed + sqliteDSNSeparator(trimmed) + strings.Join(additions, "&")
}

// SQLiteProductionDSN 给文件库 DSN 补上生产所需的全部参数。
//
// 内存库原样返回：WAL 在 `mode=memory` 下不可用，加上会让连接直接报错。
func SQLiteProductionDSN(dsn string) string {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" || IsSQLiteMemoryDSN(trimmed) {
		return trimmed
	}
	additions := make([]string, 0, 4)
	if !strings.Contains(trimmed, "_txlock=") {
		additions = append(additions, SQLiteTxLockImmediate)
	}
	if !strings.Contains(trimmed, "busy_timeout") {
		additions = append(additions, SQLiteBusyTimeoutPragma)
	}
	if !strings.Contains(trimmed, "journal_mode") {
		additions = append(additions, SQLiteJournalModePragma)
	}
	if !strings.Contains(trimmed, "synchronous") {
		additions = append(additions, SQLiteSynchronousPragma)
	}
	if len(additions) == 0 {
		return trimmed
	}
	return trimmed + sqliteDSNSeparator(trimmed) + strings.Join(additions, "&")
}

// IsSQLiteMemoryDSN 判断是否为内存库。
func IsSQLiteMemoryDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return strings.Contains(lower, ":memory:") || strings.Contains(lower, "mode=memory")
}

func sqliteDSNSeparator(dsn string) string {
	if strings.Contains(dsn, "?") {
		return "&"
	}
	return "?"
}
