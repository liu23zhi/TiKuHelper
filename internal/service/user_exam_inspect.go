package service

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	// The bundled driver registers itself as "sqlite". It is the same pure-Go
	// driver the rest of the project uses, so no cgo and no extra toolchain.
	_ "github.com/glebarez/go-sqlite"
)

// UserExamInfo 是考试客户端本地库的两张已知表。
//
// 该库默认用 SQLite3MC 加密，服务端**不做**解密：加密能力只留在管理员
// 下载的脱机工具里。这里只读取管理员已经解密并上传的明文副本。
const (
	userExamBaseInfoTable   = "baseInfo"
	userExamMacAddressTable = "macAddress"
)

// ErrUserExamNotPlaintext 表示上传的文件仍是加密库，或根本不是 SQLite。
var ErrUserExamNotPlaintext = errors.New("上传的文件不是明文 SQLite 数据库")

// ErrUserExamTooLarge 表示上传文件超过只读预览允许的大小。
var ErrUserExamTooLarge = errors.New("数据库文件超过只读预览允许的大小")

// MaxUserExamInspectBytes 是只读预览接受的最大文件大小。
// 该库正常情况下只有几十 KB，给到 16 MiB 已经非常宽松。
const MaxUserExamInspectBytes = int64(16 << 20)

var sqliteHeader = []byte("SQLite format 3\x00")

// UserExamTable 是一张表的只读投影。
type UserExamTable struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
}

// UserExamInspectResult 是 /admin/userexam/inspect 的响应体。
type UserExamInspectResult struct {
	Tables      map[string]UserExamTable `json:"tables"`
	OtherTables []string                 `json:"otherTables"`
	RowCounts   map[string]int           `json:"rowCounts"`
}

// InspectUserExamDatabase 只读解析一个**已解密**的 UserExamInfo 库。
//
// 行为约束：
//   - 只读打开（mode=ro + immutable），不写入、不产生 -wal/-shm；
//   - 只读已知的两张表，其它表只返回表名；
//   - 调用方负责传入已经落在临时目录、随后会被删除的文件路径。
func InspectUserExamDatabase(path string) (UserExamInspectResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return UserExamInspectResult{}, fmt.Errorf("%w：无法读取文件", ErrUserExamNotPlaintext)
	}
	if info.Size() > MaxUserExamInspectBytes {
		return UserExamInspectResult{}, ErrUserExamTooLarge
	}
	if !isPlaintextSQLite(path) {
		return UserExamInspectResult{}, ErrUserExamNotPlaintext
	}

	// mode=ro 保证不写；immutable=1 让驱动不尝试创建 -wal/-shm 旁文件。
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return UserExamInspectResult{}, fmt.Errorf("%w：无法打开数据库", ErrUserExamNotPlaintext)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		return UserExamInspectResult{}, fmt.Errorf("%w：数据库无法读取", ErrUserExamNotPlaintext)
	}

	result := UserExamInspectResult{
		Tables:    map[string]UserExamTable{},
		RowCounts: map[string]int{},
	}

	names, err := sqliteTableNames(db)
	if err != nil {
		return UserExamInspectResult{}, err
	}
	for _, name := range names {
		switch name {
		case userExamBaseInfoTable, userExamMacAddressTable:
			table, rows, err := readUserExamTable(db, name)
			if err != nil {
				return UserExamInspectResult{}, err
			}
			result.Tables[name] = table
			result.RowCounts[name] = rows
		default:
			result.OtherTables = append(result.OtherTables, name)
		}
	}
	return result, nil
}

func isPlaintextSQLite(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	header := make([]byte, len(sqliteHeader))
	if _, err := file.Read(header); err != nil {
		return false
	}
	return string(header) == string(sqliteHeader)
}

func sqliteTableNames(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("%w：无法读取表结构", ErrUserExamNotPlaintext)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("%w：表名读取失败", ErrUserExamNotPlaintext)
		}
		if strings.HasPrefix(name, "sqlite_") {
			continue
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func readUserExamTable(db *sql.DB, name string) (UserExamTable, int, error) {
	// 表名来自 sqlite_master，且只在本函数的白名单分支被调用，不存在注入面。
	rows, err := db.Query(`SELECT * FROM "` + name + `"`)
	if err != nil {
		return UserExamTable{}, 0, fmt.Errorf("%w：表 %s 读取失败", ErrUserExamNotPlaintext, name)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return UserExamTable{}, 0, fmt.Errorf("%w：表 %s 列信息读取失败", ErrUserExamNotPlaintext, name)
	}
	table := UserExamTable{Columns: columns, Rows: []map[string]any{}}
	count := 0
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return UserExamTable{}, 0, fmt.Errorf("%w：表 %s 行读取失败", ErrUserExamNotPlaintext, name)
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = normalizeUserExamValue(values[i])
		}
		table.Rows = append(table.Rows, row)
		count++
	}
	return table, count, rows.Err()
}

// normalizeUserExamValue 把驱动返回的原始类型收窄成可 JSON 序列化的类型，
// 并把二进制列转成十六进制字符串，避免响应里出现 base64 或原始字节。
func normalizeUserExamValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return fmt.Sprintf("%x", typed)
	case int64, float64, string, bool:
		return typed
	default:
		return fmt.Sprintf("%v", typed)
	}
}
