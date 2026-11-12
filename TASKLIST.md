# TIKU 开发清单

> 只列**进行中**的工作与已知缺口。完整清单、决策历史与内部细节存放在内部文档室（不在此提供链接）。

## 进行中

| PR | 内容 | 状态 |
|---|---|---|
| #1 | Go 依赖批量更新（dependabot） | CI 未通过：`go-sql-driver/mysql` 1.8.1→1.10.1 后 `TestNormalizeMySQLDSNFillsRequiredParameters` / `...RespectsExplicitLoc` 失败（`dsn_test.go:51` 报 `charset = "", want utf8mb4`；`:86` 缺参填充失效）。同上 PR 的 Docker 构建与前端检查均 pass |
| #2 | Web 依赖批量更新（dependabot） | CI 未通过：`@babel/core` 7.29.7→8.1.0，`@vue/cli-plugin-babel@5` 仍要求 Babel ^7，编译期报 `Requires Babel "^7.0.0-0", but was loaded with "8.1.0"`。Docker 构建、Vue 前端检查、frontend 三个 job 失败 |

## 已合并（本轮）

- #24 子进程环境按平台构造 · #25 `GiftBalance` 注释旧口径 · #26 `.gitattributes` 统一 LF
- #27 `normalizeUserStatus` 吞 `banned`（P0 静默失效）· #28 绑定历史三连 · #29 返佣账本四类聚合
- #30 质量债（日志按年龄清理 / trace 四字段 / 并发 DSN 分类）· #31 用户等级（`level_rank`）· #32 `/admin/security-status` 鉴权
- #33 括号空白无关召回 + 行可用性判定唯一化 · #34 本地 gofmt 区分真格式与 CRLF 假阳性

## 已知缺口

- 否定题判成同一道题、题库软删除后仍可搜到，两类问题已修复（#14 / #7）并已在 main，不再列为缺口
- #3（`fix/startup-policy-trusted-proxies`）已 CLOSED，内容已在 main；远端分支已删，无需重开

## 约定

- 每个 PR 一个功能，可独立审阅
- 合并前 CI 必须全绿，或说明哪些是既有失败
