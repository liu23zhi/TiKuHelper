## 自定义配置

在程序运行的**根目录**创建 `config.yaml`，程序会自动读取。全部字段与默认值见本目录的
`config.yaml`。

### 限流

按来源 IP 限流，默认开启：60 秒窗口内每个 IP 最多 120 次请求（`limit.enable` /
`duration` / `requests`）。

### JWT 与代理

`JWT_SECRET` 至少 32 字节，环境变量优先并写入数据库；为空时复用数据库密钥，没有有效密钥时
自动生成保存。

`TRUSTED_PROXIES` 为空时**信任所有代理转发头**；设置后仅信任逗号分隔的代理 IP/CIDR。
反代部署时这个方向不能配错。

### 数据库

默认 SQLite3，也支持 MySQL（配置文件 `mysql` 字段或 `SQL_DSN` 环境变量）。

**优先级（环境变量最高）**：

```
SQL_DSN 环境变量  >  config.yaml 的 mysql 字段  >  SQLite（database.path，默认 tiku.db）
```

两者都配置时以环境变量为准，启动日志会提示不一致（不打印 DSN 本体）。建议 MySQL 8.0 +
InnoDB；项目含多个 utf8mb4 复合索引，旧版需确认大索引前缀与 `DYNAMIC` 行格式已启用，
否则迁移可能建不出索引。

连接池由 `database.mysqlPool` 控制（默认 `maxOpenConns: 32`、`maxIdleConns: 8`、连接最长
30 分钟、空闲 5 分钟），`maxOpenConns` 同时是优先级调度器并发上限。DSN 参数会在建连前自动
补齐 `parseTime=true`、`loc=Asia/Shanghai`、`charset=utf8mb4`（缺 `parseTime` 会启动即崩）。

### 自定义外部题库

字段含义：`url` 接口地址、`method` 请求方法、`headers` 请求头（`Content-Type` 必须正确）、
`body` 请求体（支持占位符 `${question}`、`${type}`）、`answer` 用 jsonpath 取答案的路径。
`enable: false` 时不生效。内置样例渠道已集成，无需重复配置。

### 自动录入题库

脚本答题或调 API 时会自动记录问题与答案；`recordEmptyAnswer: true` 时连空答案也记录。
