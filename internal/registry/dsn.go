package registry

import (
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// MySQL DSN 自动补齐的相关说明。
//
// 线上故障（切换 MySQL 后容器反复重启）根因就是 DSN 少了 parseTime：
//
//	Scan error on column index 4, name "created_at":
//	unsupported Scan, storing driver.Value type []uint8 into type *time.Time
//
// go-sql-driver 在没有 parseTime=true 时把 DATETIME 当 []byte 返回，
// GORM 扫描 time.Time 直接失败；而 gorm.io/driver/mysql 只 ParseDSN、
// 不注入任何默认值（见 mysql@v1.6.0/mysql.go:70），于是：
//
//	system_settings 读取失败 → 迁移标记读不到（每次启动重跑）→
//	loadStoredJWTSecret 报错 → CreateManager 返回错误 → FatalLog → 容器重启循环
//
// 这里在建立连接前把必需参数补齐，任何来源（SQL_DSN / config.yaml）的 DSN
// 都不再因为漏参数而炸。

// dsnQueryParams 返回 DSN 查询串里出现过的参数名（小写）。
// 用 LastIndexByte 与 go-sql-driver 的 ParseDSN 保持一致的分隔位置。
func dsnQueryParams(dsn string) map[string]bool {
	params := map[string]bool{}
	index := strings.LastIndexByte(dsn, '?')
	if index < 0 {
		return params
	}
	for _, pair := range strings.Split(dsn[index+1:], "&") {
		name := pair
		if separator := strings.IndexByte(pair, '='); separator >= 0 {
			name = pair[:separator]
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			params[name] = true
		}
	}
	return params
}

// normalizeMySQLDSN 补齐 go-sql-driver 必需参数，返回新 DSN 与被补齐的项。
//
// 规则：
//   - parseTime 一律为 true（无它则时间列无法扫描，服务起不来）；
//   - 未显式给 loc 时用进程时区（本服务固定 Asia/Shanghai，见 useShanghaiTime）；
//     显式给了 loc 就尊重（仅在日志提醒可能与进程时区不一致）；
//   - 未显式给 charset/collation 时补 charset=utf8mb4。
//
// 解析失败时原样返回：不要因为 DSN 格式奇怪就吞掉 gorm 原本能给出的报错。
func normalizeMySQLDSN(dsn string) (string, []string) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return dsn, nil
	}
	config, err := mysqldriver.ParseDSN(trimmed)
	if err != nil {
		return dsn, []string{"DSN 解析失败，按原样使用：" + err.Error()}
	}

	present := dsnQueryParams(trimmed)
	var fixes []string
	if !config.ParseTime {
		config.ParseTime = true
		fixes = append(fixes, "parseTime=true")
	}
	if !present["loc"] {
		config.Loc = time.Local
		fixes = append(fixes, "loc="+config.Loc.String())
	}
	if !present["charset"] && !present["collation"] {
		if config.Params == nil {
			config.Params = map[string]string{}
		}
		config.Params["charset"] = "utf8mb4"
		fixes = append(fixes, "charset=utf8mb4")
	}
	if len(fixes) == 0 {
		return trimmed, nil
	}
	return config.FormatDSN(), fixes
}
