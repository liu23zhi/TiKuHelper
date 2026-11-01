#!/usr/bin/env python3
"""Migrate a tikuAdapter SQLite database into an empty MySQL database.

The source is opened read-only.  The destination must be empty by default;
tables are first created with temporary names, copied in batches, checked by
row count, and renamed in one MySQL RENAME TABLE statement.  Existing target
data is never overwritten.

PyMySQL is the only third-party dependency.  Use --auto-install-deps when the
script is allowed to install it with the current Python interpreter:

    python tools/migrate_sqlite_to_mysql.py \
      --sqlite-path data/tiku.db \
      --mysql-url mysql://user@127.0.0.1:3306/tiku \
      --auto-install-deps

Set MYSQL_PASSWORD in the environment or enter it at the prompt. Avoid putting
credentials directly into command-line arguments.

The script intentionally does not copy SQLite's internal sqlite_sequence table.
MySQL AUTO_INCREMENT values are restored after copying rows, so preserved IDs
remain valid for existing references.

Access-token quota limits outside DECIMAL(18,6) are normalized to 0 (unlimited)
while copying, matching the application startup repair behavior.
"""

from __future__ import annotations

import argparse
import datetime as dt
import getpass
import hashlib
import importlib
import os
import re
import secrets
import sqlite3
import subprocess
import sys
from decimal import Decimal
from pathlib import Path
from typing import Any, Iterable, Sequence
from urllib.parse import parse_qs, unquote, urlparse


DEPENDENCY = "PyMySQL>=1.1.1,<2"
ACCESS_TOKEN_QUOTA_LIMIT_MAX_EXCLUSIVE = Decimal("1000000000000")


def load_pymysql(auto_install: bool):
    """Import PyMySQL, optionally installing it into this interpreter."""
    try:
        return importlib.import_module("pymysql")
    except ImportError:
        if not auto_install:
            raise SystemExit(
                "缺少 PyMySQL。请先执行 `python -m pip install PyMySQL`，"
                "或重新运行并添加 --auto-install-deps。"
            )
        command = [sys.executable, "-m", "pip", "install", DEPENDENCY]
        print("正在安装缺少的依赖：", " ".join(command))
        subprocess.check_call(command)
        try:
            return importlib.import_module("pymysql")
        except ImportError as exc:
            raise SystemExit("PyMySQL 安装完成但仍无法导入，请检查 Python 环境。") from exc


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="将 tikuAdapter SQLite 数据库迁移到 MySQL")
    parser.add_argument("--sqlite-path", required=True, help="SQLite 数据库文件路径")
    parser.add_argument("--mysql-url", help="mysql://user[:password]@host:port/database；密码建议用 MYSQL_PASSWORD")
    parser.add_argument("--mysql-dsn", help="Go MySQL DSN，例如 user:pass@tcp(host:3306)/database?charset=utf8mb4")
    parser.add_argument("--mysql-host", default="127.0.0.1")
    parser.add_argument("--mysql-port", type=int, default=3306)
    parser.add_argument("--mysql-user")
    parser.add_argument("--mysql-password", help="不提供时读取 MYSQL_PASSWORD，仍为空则交互式询问；命令行密码可能泄露")
    parser.add_argument("--mysql-database")
    parser.add_argument("--batch-size", type=int, default=500, help="每批插入行数，默认 500")
    parser.add_argument("--auto-install-deps", action="store_true", help="缺少 PyMySQL 时自动调用 pip 安装")
    parser.add_argument("--yes", action="store_true", help="跳过开始迁移前的确认提示")
    parser.add_argument("--keep-staging-on-error", action="store_true", help="失败时保留临时表以便排查")
    return parser.parse_args()


def validate_mysql_query(query: str, source: str) -> None:
    values = {key.lower(): value for key, value in parse_qs(query, keep_blank_values=True).items()}
    allowed = {"charset", "parsetime", "loc"}
    unsupported = sorted(key for key in values if key.lower() not in allowed)
    if unsupported:
        raise SystemExit(
            f"{source}包含脚本不会静默忽略的参数：{', '.join(unsupported)}。"
            "请移除 TLS/SSL 等参数，或改为在 MySQL 客户端层配置安全连接。"
        )
    charset = values.get("charset", ["utf8mb4"])[0].lower()
    if charset not in {"utf8mb4", "utf-8"}:
        raise SystemExit(f"{source}的 charset 必须为 utf8mb4，当前为 {charset!r}")
    parse_time = values.get("parsetime", [])
    if parse_time and parse_time[0].lower() != "true":
        raise SystemExit(f"{source}的 parseTime 必须为 true")
    locations = values.get("loc", [])
    if locations and locations[0].lower() not in {"local", "utc"}:
        raise SystemExit(f"{source}的 loc 仅支持 Local 或 UTC")
    if parse_time or locations:
        print(f"提示：{source}中的 parseTime/loc 仅用于兼容 Go DSN；迁移写入按 UTC 处理。")


def resolve_password(value: str | None) -> str:
    if value is not None:
        print("警告：连接参数中包含密码，命令行参数可能被 shell 历史或进程列表记录。", file=sys.stderr)
        return value
    env_password = os.environ.get("MYSQL_PASSWORD")
    if env_password is not None:
        return env_password
    return getpass.getpass("MySQL 密码：")


def mysql_options(args: argparse.Namespace) -> dict[str, Any]:
    if args.mysql_dsn:
        match = re.fullmatch(r"([^:]+):(.*)@tcp\(([^)]+)\)/([^?]+)(?:\?.*)?", args.mysql_dsn)
        if not match:
            raise SystemExit("暂只支持 TCP 形式的 Go MySQL DSN：user:password@tcp(host:port)/database?...")
        user, password, address, database = match.groups()
        query = args.mysql_dsn.partition("?")[2]
        if query:
            validate_mysql_query(query, "--mysql-dsn")
        if ":" in address:
            host, port_text = address.rsplit(":", 1)
            if host.startswith("[") and host.endswith("]"):
                host = host[1:-1]
            port = int(port_text)
        else:
            host, port = address, 3306
        return {"host": host, "port": port, "user": user, "password": resolve_password(password), "database": database}
    if args.mysql_url:
        parsed = urlparse(args.mysql_url)
        if parsed.scheme.lower() not in {"mysql", "mysql+pymysql"}:
            raise SystemExit("--mysql-url 必须使用 mysql:// 或 mysql+pymysql://")
        if not parsed.hostname or not parsed.path.strip("/"):
            raise SystemExit("--mysql-url 缺少主机或数据库名")
        validate_mysql_query(parsed.query, "--mysql-url")
        password = unquote(parsed.password) if parsed.password is not None else None
        user = unquote(parsed.username or "")
        return {
            "host": parsed.hostname,
            "port": parsed.port or 3306,
            "user": user,
            "password": resolve_password(password),
            "database": unquote(parsed.path.strip("/")),
        }

    user = args.mysql_user or os.environ.get("MYSQL_USER")
    database = args.mysql_database or os.environ.get("MYSQL_DATABASE")
    if not user or not database:
        raise SystemExit("请提供 --mysql-url/--mysql-dsn，或同时提供 --mysql-user/--mysql-database")
    password = resolve_password(args.mysql_password)
    return {
        "host": args.mysql_host,
        "port": args.mysql_port,
        "user": user,
        "password": password,
        "database": database,
    }


def quote_mysql(identifier: str) -> str:
    if "\x00" in identifier:
        raise ValueError("标识符包含 NUL 字符")
    return "`" + identifier.replace("`", "``") + "`"


def quote_sqlite(identifier: str) -> str:
    return '"' + identifier.replace('"', '""') + '"'


def safe_staging_name(table_name: str, run_nonce: str = "") -> str:
    digest = hashlib.sha256(table_name.encode("utf-8")).hexdigest()[:10]
    readable = re.sub(r"[^0-9A-Za-z_]+", "_", table_name)[:40].strip("_") or "table"
    nonce = re.sub(r"[^0-9A-Za-z]+", "", run_nonce)[:10]
    prefix = f"__tiku_migrate_{nonce}_" if nonce else "__tiku_migrate_"
    return f"{prefix}{digest}_{readable}"[:64]


def sqlite_tables(source: sqlite3.Connection) -> list[str]:
    rows = source.execute(
        "SELECT name FROM sqlite_master WHERE type = 'table' AND lower(substr(name, 1, 7)) <> 'sqlite_' ORDER BY name"
    ).fetchall()
    return [row[0] for row in rows]


def sqlite_columns(source: sqlite3.Connection, table_name: str) -> list[dict[str, Any]]:
    rows = source.execute(f"PRAGMA table_info({quote_sqlite(table_name)})").fetchall()
    if not rows:
        raise RuntimeError(f"SQLite 表不存在或没有列：{table_name}")
    return [
        {"cid": row[0], "name": row[1], "type": row[2] or "TEXT", "notnull": bool(row[3]), "default": row[4], "pk": int(row[5])}
        for row in rows
    ]


def sqlite_indexes(source: sqlite3.Connection, table_name: str) -> list[dict[str, Any]]:
    indexes: list[dict[str, Any]] = []
    for row in source.execute(f"PRAGMA index_list({quote_sqlite(table_name)})").fetchall():
        # seq, name, unique, origin, partial (the last field is absent on old SQLite).
        name, unique = row[1], bool(row[2])
        origin = row[3] if len(row) > 3 else "c"
        partial = bool(row[4]) if len(row) > 4 else False
        if partial:
            raise RuntimeError(f"不支持 SQLite 部分索引：{table_name}.{name}")
        if origin == "pk":
            continue
        detail = source.execute(f"PRAGMA index_xinfo({quote_sqlite(name)})").fetchall()
        key_parts = [item for item in detail if len(item) < 6 or item[5] == 1]
        if any(item[2] is None or item[1] < 0 for item in key_parts):
            raise RuntimeError(f"不支持 SQLite 表达式索引：{table_name}.{name}")
        if any(str(item[4]).upper() not in {"BINARY", "NONE"} for item in key_parts):
            raise RuntimeError(f"不支持非 BINARY SQLite 索引排序语义：{table_name}.{name}")
        columns = [item[2] for item in key_parts]
        if columns:
            indexes.append({"name": name, "unique": unique, "columns": columns})
    return indexes


def normalize_declared_type(declared: str) -> str:
    return re.sub(r"\s+", " ", declared.strip()).upper()


def mysql_type(column: dict[str, Any], primary_key_columns: set[str]) -> str:
    declared = normalize_declared_type(column["type"])
    name = column["name"]
    if name in primary_key_columns and declared.startswith("INTEGER"):
        # BIGINT safely preserves both int32 and int64 IDs used by the service.
        return "BIGINT"
    if declared.startswith("INT") or declared.startswith("INTEGER"):
        return "BIGINT"
    if declared.startswith("BIGINT"):
        return "BIGINT"
    if declared.startswith("TINYINT"):
        return "TINYINT"
    if declared.startswith("SMALLINT"):
        return "SMALLINT"
    if declared.startswith("DECIMAL") or declared.startswith("NUMERIC"):
        if not re.search(r"\(\s*\d+", declared):
            raise RuntimeError(
                f"SQLite 数值列 {name!r} 未声明精度，无法保证 MySQL DECIMAL 无损映射；请先显式定义精度"
            )
        return declared.replace("NUMERIC", "DECIMAL", 1)
    if declared.startswith("DOUBLE") or declared.startswith("REAL") or declared.startswith("FLOAT"):
        return "DOUBLE"
    if declared.startswith("BOOL"):
        return "TINYINT(1)"
    if declared.startswith("CHAR"):
        if re.search(r"\(\s*\d+", declared):
            return declared
        return "LONGTEXT"
    if declared.startswith("VARCHAR"):
        if re.search(r"\(\s*\d+", declared):
            return declared
        return "LONGTEXT"
    if declared.startswith("DATE") and not declared.startswith("DATETIME"):
        return "DATE"
    if declared.startswith("TIMESTAMP") or declared.startswith("DATETIME"):
        return "DATETIME(6)"
    if declared.startswith("TIME"):
        return "TIME(6)"
    if declared.startswith("BLOB"):
        return "LONGBLOB"
    # SQLite TEXT has no length limit; LONGTEXT also keeps JSON and audit bodies intact.
    return "LONGTEXT"


def sqlite_default_sql(value: Any, target_type: str) -> str | None:
    if value is None:
        return None
    raw = str(value).strip()
    if target_type in {"LONGTEXT", "LONGBLOB"}:
        raise RuntimeError(f"MySQL 不支持安全保留此大字段默认值：{raw!r}")
    if raw.upper() in {"NULL", "CURRENT_TIMESTAMP", "CURRENT_DATE", "CURRENT_TIME"}:
        return raw.upper()
    if target_type.startswith(("BIGINT", "INT", "TINYINT", "SMALLINT", "DECIMAL", "DOUBLE", "FLOAT")):
        if re.fullmatch(r"[-+]?\d+(?:\.\d+)?", raw):
            return raw
    if raw.lower() in {"true", "false"}:
        return "1" if raw.lower() == "true" else "0"
    # Decode SQLite's quoted literal syntax, then escape it for MySQL. In
    # particular, a backslash must not become a MySQL escape sequence.
    if len(raw) >= 2 and raw[0] == raw[-1] == "'":
        literal = raw[1:-1].replace("''", "'")
        return "'" + literal.replace("\\", "\\\\").replace("'", "''") + "'"
    if len(raw) >= 2 and raw[0] == raw[-1] == '"':
        literal = raw[1:-1].replace('""', '"')
        return "'" + literal.replace("\\", "\\\\").replace("'", "''") + "'"
    raise RuntimeError(f"不支持此 SQLite 默认表达式：{raw!r}")


def create_table_sql(staging_name: str, columns: list[dict[str, Any]]) -> str:
    primary = {column["name"] for column in columns if column["pk"] > 0}
    ordered_primary = [column["name"] for column in sorted(columns, key=lambda item: item["pk"]) if column["pk"] > 0]
    definitions: list[str] = []
    for column in columns:
        target_type = mysql_type(column, primary)
        parts = [quote_mysql(column["name"]), target_type]
        if column["pk"] > 0 and len(ordered_primary) == 1 and normalize_declared_type(column["type"]).startswith("INTEGER"):
            parts.append("AUTO_INCREMENT")
        if column["notnull"] or column["pk"] > 0:
            parts.append("NOT NULL")
        else:
            parts.append("NULL")
        default_sql = sqlite_default_sql(column["default"], target_type)
        if default_sql is not None:
            parts.extend(["DEFAULT", default_sql])
        definitions.append(" ".join(parts))
    if ordered_primary:
        definitions.append("PRIMARY KEY (" + ", ".join(quote_mysql(name) for name in ordered_primary) + ")")
    return (
        f"CREATE TABLE {quote_mysql(staging_name)} ("
        + ", ".join(definitions)
        + ") ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
    )


def parse_datetime(value: Any) -> Any:
    if not isinstance(value, str):
        return value
    text = value.strip()
    if not text:
        return value
    candidate = text.replace("Z", "+00:00")
    try:
        parsed = dt.datetime.fromisoformat(candidate)
    except ValueError:
        return value
    # MySQL DATETIME has no timezone. Preserve the stored wall-clock value.
    return parsed.replace(tzinfo=None) if parsed.tzinfo else parsed


def convert_value(value: Any, declared_type: str) -> Any:
    if value is None:
        return None
    normalized = normalize_declared_type(declared_type)
    if "DATE" in normalized or "TIME" in normalized:
        return parse_datetime(value)
    if "DECIMAL" in normalized or "NUMERIC" in normalized:
        try:
            return Decimal(str(value))
        except Exception:
            return value
    if isinstance(value, memoryview):
        return value.tobytes()
    return value


def normalize_access_token_quota_limit(value: Any) -> tuple[Any, bool]:
    """Map unrepresentable DECIMAL(18,6) quotas to the app's unlimited value."""
    if value is None:
        return value, False
    try:
        quota = Decimal(str(value))
    except Exception:
        return value, False
    if not quota.is_finite() or quota >= ACCESS_TOKEN_QUOTA_LIMIT_MAX_EXCLUSIVE or quota <= -ACCESS_TOKEN_QUOTA_LIMIT_MAX_EXCLUSIVE:
        return Decimal(0), True
    return value, False


def mysql_server_info(connection) -> tuple[tuple[int, int, int], str]:
    with connection.cursor() as cursor:
        cursor.execute("SELECT VERSION(), @@character_set_server, @@collation_server")
        version, charset, collation = cursor.fetchone()
    match = re.search(r"(\d+)\.(\d+)\.(\d+)", str(version))
    parsed = tuple(int(item) for item in match.groups()) if match else (0, 0, 0)
    if "mariadb" in str(version).lower():
        maria_match = re.search(r"(\d+)\.(\d+)\.(\d+)-MariaDB", str(version), re.IGNORECASE)
        if maria_match:
            parsed = tuple(int(item) for item in maria_match.groups())
    print(f"MySQL: {version}; 字符集: {charset}/{collation}")
    return parsed, str(version)


def configure_mysql_session(connection) -> None:
    """Require strict writes and preserve explicit SQLite zero primary keys."""
    with connection.cursor() as cursor:
        cursor.execute("SELECT @@SESSION.sql_mode")
        current = cursor.fetchone()[0] or ""
        modes = {item for item in str(current).split(",") if item}
        modes.discard("NO_BACKSLASH_ESCAPES")
        modes.discard("ALLOW_INVALID_DATES")
        modes.update({"STRICT_ALL_TABLES", "NO_AUTO_VALUE_ON_ZERO"})
        cursor.execute("SET SESSION sql_mode = %s", (",".join(sorted(modes)),))
        cursor.execute("SET SESSION time_zone = '+00:00'")
        cursor.execute("SELECT @@SESSION.sql_mode")
        effective = {item for item in str(cursor.fetchone()[0] or "").split(",") if item}
    required = {"STRICT_ALL_TABLES", "NO_AUTO_VALUE_ON_ZERO"}
    if not required.issubset(effective):
        raise RuntimeError("无法启用 STRICT_ALL_TABLES 与 NO_AUTO_VALUE_ON_ZERO，拒绝继续迁移")
    print("MySQL 会话已启用严格写入和 NO_AUTO_VALUE_ON_ZERO。")


def check_mysql_capabilities(connection, source: sqlite3.Connection, tables: Sequence[str]) -> None:
    version, raw_version = mysql_server_info(connection)
    if version == (0, 0, 0):
        raise RuntimeError(f"无法解析 MySQL 版本：{raw_version}")
    with connection.cursor() as cursor:
        cursor.execute("SELECT @@character_set_database, @@collation_database, @@innodb_page_size")
        charset, collation, page_size = cursor.fetchone()
        if str(charset).lower() != "utf8mb4":
            print(f"警告：目标数据库字符集为 {charset}，脚本会在临时表上指定 utf8mb4。")
        cursor.execute("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()")
        count = int(cursor.fetchone()[0])
        if count:
            raise RuntimeError("目标数据库不是空库，默认拒绝迁移以避免覆盖现有数据。")
    # Older InnoDB limits can reject the composite indexes used by this app.
    risky = []
    modern_index_limit = min(3072, (int(page_size) // 1024) * 192)

    def estimate_index_bytes(columns: list[dict[str, Any]], names: Sequence[str], unique: bool, label: str) -> int:
        total_bytes = 0
        for name in names:
            column = next(column for column in columns if column["name"] == name)
            declared = normalize_declared_type(column["type"])
            length_match = re.search(r"\((\d+)\)", declared)
            if declared.startswith(("VARCHAR", "CHAR")) and length_match:
                total_bytes += int(length_match.group(1)) * 4
            elif "TEXT" in declared or "CLOB" in declared:
                if unique:
                    raise RuntimeError(f"MySQL 无法安全表达 LONGTEXT 唯一索引：{label}")
                total_bytes += 191 * 4
            else:
                total_bytes += 8
        if total_bytes > modern_index_limit:
            raise RuntimeError(f"索引超过此 InnoDB 页大小支持的 {modern_index_limit}-byte 上限：{label}")
        return total_bytes

    for table in tables:
        columns = sqlite_columns(source, table)
        primary = [column["name"] for column in sorted(columns, key=lambda item: item["pk"]) if column["pk"] > 0]
        if primary:
            total_bytes = estimate_index_bytes(columns, primary, True, f"{table}.PRIMARY")
            if total_bytes > 767:
                risky.append(f"{table}.PRIMARY")
        for index in sqlite_indexes(source, table):
            total_bytes = estimate_index_bytes(columns, index["columns"], index["unique"], f"{table}.{index['name']}")
            if total_bytes > 767:
                risky.append(f"{table}.{index['name']}")
    is_mariadb = "mariadb" in raw_version.lower()
    old_index_limit = version < (10, 2, 2) if is_mariadb else version < (5, 7, 7)
    if risky and old_index_limit:
        raise RuntimeError(
            "目标 MySQL/MariaDB 版本可能受 767-byte 索引前缀限制："
            + ", ".join(risky)
            + "。请升级到 MySQL 5.7.7+ 或启用 InnoDB 大索引前缀。"
        )


def copy_table(source: sqlite3.Connection, target, table_name: str, staging_name: str, batch_size: int) -> int:
    columns = sqlite_columns(source, table_name)
    names = [column["name"] for column in columns]
    source_sql = f"SELECT {', '.join(quote_sqlite(name) for name in names)} FROM {quote_sqlite(table_name)}"
    insert_sql = (
        f"INSERT INTO {quote_mysql(staging_name)} ({', '.join(quote_mysql(name) for name in names)}) "
        f"VALUES ({', '.join(['%s'] * len(names))})"
    )
    count = 0
    corrected_quotas = 0
    cursor = source.execute(source_sql)
    while True:
        rows = cursor.fetchmany(batch_size)
        if not rows:
            break
        converted = []
        for row in rows:
            converted_row = []
            for value, column in zip(row, columns):
                converted_value = convert_value(value, column["type"])
                if table_name.lower() == "access_tokens" and column["name"].lower() == "total_quota_limit":
                    converted_value, corrected = normalize_access_token_quota_limit(converted_value)
                    corrected_quotas += int(corrected)
                converted_row.append(converted_value)
            converted.append(tuple(converted_row))
        with target.cursor() as target_cursor:
            target_cursor.executemany(insert_sql, converted)
            target_cursor.execute("SHOW WARNINGS LIMIT 1")
            warning = target_cursor.fetchone()
            if warning:
                raise RuntimeError(f"{table_name} 批量写入产生 MySQL 警告，已拒绝发布：{warning}")
        target.commit()
        count += len(rows)
        print(f"  {table_name}: {count} 行")
    if corrected_quotas:
        print(f"  {table_name}.total_quota_limit: {corrected_quotas} 个超范围值已修正为 0（无限额）")
    return count


def create_indexes(source: sqlite3.Connection, target, table_name: str, staging_name: str) -> None:
    columns_by_name = {column["name"]: column for column in sqlite_columns(source, table_name)}
    for index in sqlite_indexes(source, table_name):
        indexed_columns = []
        for name in index["columns"]:
            column = columns_by_name[name]
            declared = normalize_declared_type(column["type"])
            if "TEXT" in declared or "CLOB" in declared:
                if index["unique"]:
                    raise RuntimeError(f"MySQL 无法安全表达 LONGTEXT 唯一索引：{table_name}.{index['name']}")
                indexed_columns.append(f"{quote_mysql(name)}(191)")
            else:
                indexed_columns.append(quote_mysql(name))
        columns = ", ".join(indexed_columns)
        unique = "UNIQUE " if index["unique"] else ""
        statement = f"CREATE {unique}INDEX {quote_mysql(index['name'])} ON {quote_mysql(staging_name)} ({columns})"
        with target.cursor() as cursor:
            cursor.execute(statement)
        target.commit()


def set_auto_increment(source: sqlite3.Connection, target, table_name: str, staging_name: str) -> None:
    columns = sqlite_columns(source, table_name)
    primary = [column for column in columns if column["pk"] > 0]
    if len(primary) != 1 or not normalize_declared_type(primary[0]["type"]).startswith("INTEGER"):
        return
    row = source.execute(f"SELECT MAX({quote_sqlite(primary[0]['name'])}) FROM {quote_sqlite(table_name)}").fetchone()
    next_value = int(row[0]) + 1 if row and row[0] is not None else 1
    sequence_table = source.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'sqlite_sequence'"
    ).fetchone()
    if sequence_table:
        sequence = source.execute("SELECT seq FROM sqlite_sequence WHERE name = ?", (table_name,)).fetchone()
        if sequence and sequence[0] is not None:
            next_value = max(next_value, int(sequence[0]) + 1)
    if next_value > 1 or (row and row[0] is not None):
        with target.cursor() as cursor:
            cursor.execute(f"ALTER TABLE {quote_mysql(staging_name)} AUTO_INCREMENT = %d" % next_value)
        target.commit()


def drop_staging(target, staging_names: Iterable[str]) -> None:
    for staging_name in staging_names:
        try:
            with target.cursor() as cursor:
                cursor.execute(f"DROP TABLE IF EXISTS {quote_mysql(staging_name)}")
            target.commit()
        except Exception as exc:
            print(f"警告：清理临时表 {staging_name} 失败：{exc}", file=sys.stderr)


def migrate(args: argparse.Namespace) -> None:
    if sys.version_info < (3, 10):
        raise SystemExit("此脚本需要 Python 3.10 或更新版本。")
    if args.batch_size < 1 or args.batch_size > 10000:
        raise SystemExit("--batch-size 必须在 1 到 10000 之间")
    if not os.path.isfile(args.sqlite_path):
        raise SystemExit(f"SQLite 文件不存在：{args.sqlite_path}")
    pymysql = load_pymysql(args.auto_install_deps)
    options = mysql_options(args)
    source_uri = Path(args.sqlite_path).resolve().as_uri() + "?mode=ro"
    source = sqlite3.connect(source_uri, uri=True)
    source.row_factory = None
    source.execute("PRAGMA query_only=ON")
    source.execute("BEGIN")
    target = None
    staging: list[str] = []
    run_nonce = secrets.token_hex(5)
    try:
        tables = sqlite_tables(source)
        if not tables:
            raise RuntimeError("SQLite 数据库没有可迁移的业务表。")
        unsupported = source.execute(
            "SELECT type, name FROM sqlite_master WHERE type IN ('view', 'trigger') AND lower(substr(name, 1, 7)) <> 'sqlite_'"
        ).fetchall()
        if unsupported:
            raise RuntimeError("源库包含当前脚本不迁移的视图或触发器：" + ", ".join(row[1] for row in unsupported))
        for table_name in tables:
            ddl = source.execute("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", (table_name,)).fetchone()[0] or ""
            if re.search(r"\bCHECK\s*\(", ddl, re.IGNORECASE):
                raise RuntimeError(f"源表包含未迁移的 CHECK 约束：{table_name}")
            if re.search(r"\bCOLLATE\s+(?!BINARY\b)[A-Za-z_]+", ddl, re.IGNORECASE):
                raise RuntimeError(f"源表包含与目标 utf8mb4_bin 不同的列排序规则：{table_name}")
            if re.search(r"\bON\s+CONFLICT\b", ddl, re.IGNORECASE):
                raise RuntimeError(f"源表包含未迁移的冲突处理约束：{table_name}")
            if source.execute(f"PRAGMA foreign_key_list({quote_sqlite(table_name)})").fetchone():
                raise RuntimeError(f"源表包含外键；为避免静默丢约束，本脚本拒绝迁移：{table_name}")
            if any(len(row) > 6 and row[6] != 0 for row in source.execute(f"PRAGMA table_xinfo({quote_sqlite(table_name)})").fetchall()):
                raise RuntimeError(f"源表包含生成列或隐藏列：{table_name}")
        print(f"发现 {len(tables)} 张业务表：{', '.join(tables)}")
        target = pymysql.connect(**options, charset="utf8mb4", autocommit=False, connect_timeout=10)
        configure_mysql_session(target)
        check_mysql_capabilities(target, source, tables)
        if not args.yes:
            answer = input("将把所有行复制到空 MySQL 库并创建原表名，是否继续？输入 yes 确认：").strip().lower()
            if answer != "yes":
                print("已取消，未创建目标表。")
                return
        rename_pairs: list[tuple[str, str]] = []
        for table_name in tables:
            staging_name = safe_staging_name(table_name, run_nonce)
            columns = sqlite_columns(source, table_name)
            with target.cursor() as cursor:
                cursor.execute(create_table_sql(staging_name, columns))
            staging.append(staging_name)
            target.commit()
            copy_table(source, target, table_name, staging_name, args.batch_size)
            create_indexes(source, target, table_name, staging_name)
            set_auto_increment(source, target, table_name, staging_name)
            with target.cursor() as cursor:
                cursor.execute(f"SELECT COUNT(*) FROM {quote_mysql(staging_name)}")
                copied = int(cursor.fetchone()[0])
            expected = int(source.execute(f"SELECT COUNT(*) FROM {quote_sqlite(table_name)}").fetchone()[0])
            if copied != expected:
                raise RuntimeError(f"{table_name} 行数校验失败：源 {expected}，目标 {copied}")
            rename_pairs.append((staging_name, table_name))
        with target.cursor() as cursor:
            cursor.execute("RENAME TABLE " + ", ".join(f"{quote_mysql(old)} TO {quote_mysql(new)}" for old, new in rename_pairs))
        target.commit()
        staging.clear()
        print("迁移完成：所有表已复制并通过行数校验。")
    except KeyboardInterrupt:
        if target is not None and not args.keep_staging_on_error:
            drop_staging(target, staging)
        raise
    except Exception:
        if target is not None and not args.keep_staging_on_error:
            drop_staging(target, staging)
        raise
    finally:
        source.close()
        if target is not None:
            target.close()


def main() -> int:
    args = parse_args()
    try:
        migrate(args)
    except KeyboardInterrupt:
        print("已取消。", file=sys.stderr)
        return 130
    except Exception as exc:
        print(f"迁移失败：{exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
