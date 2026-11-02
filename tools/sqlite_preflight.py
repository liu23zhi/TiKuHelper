#!/usr/bin/env python3
"""迁移前对 SQLite 源库做 MySQL 兼容性预检。

为什么需要它：migrate_sqlite_to_mysql.py 会拒绝一部分源库（视图/触发器/外键/
生成列/CHECK），而 SQLite 不校验 varchar(n) 与 NOT NULL 的实际数据，直接搬到
MySQL 严格模式下会中途报错。先跑一遍预检，把「迁移会失败」提前暴露出来。

典型用法：

    python tools/sqlite_preflight.py --sqlite-path data/tiku.db \\
      --export-long logs/question_text-over512.json

退出码：0 = 没有阻断项（可能有提醒），2 = 有阻断项，1 = 参数/读取错误。

阻断项（迁移会失败或 MySQL 严格模式会拒绝）：
- 视图 / 触发器 / 外键 / 生成列或隐藏列 / CHECK 约束
- NOT NULL 列里存在 NULL
- varchar(n)/char(n) 声明长度小于实际最长数据（MySQL 严格模式 1406）
提醒项（能迁移，但要知道后果）：
- numeric 无小数位的列出现非 0/1 取值（脚本会映射成 DOUBLE）
- 时间文本混用 +00:00 / +08:00 与 9 位纳秒（写库时按墙钟截断到微秒）
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import sqlite3
import sys
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mysql_migration_common as common  # noqa: E402

TEXT_TYPES = ("VARCHAR", "CHAR", "TEXT", "CLOB", "LONGTEXT")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="迁移前 SQLite → MySQL 兼容性预检")
    parser.add_argument("--sqlite-path", required=True, help="SQLite 数据库文件路径（只读打开）")
    parser.add_argument("--export-long", help="把超出声明长度的字段原文导出到该 JSON 文件")
    parser.add_argument("--json", action="store_true", help="以 JSON 输出检查结果")
    return parser.parse_args()


def open_readonly(path: str) -> sqlite3.Connection:
    target = Path(path)
    if not target.is_file():
        raise SystemExit("SQLite 文件不存在：%s" % path)
    connection = sqlite3.connect(target.resolve().as_uri() + "?mode=ro", uri=True)
    connection.execute("PRAGMA query_only=ON")
    return connection


def list_tables(connection: sqlite3.Connection) -> list[str]:
    rows = connection.execute(
        "SELECT name FROM sqlite_master WHERE type = 'table' AND lower(substr(name, 1, 7)) <> 'sqlite_' "
        "ORDER BY name"
    ).fetchall()
    return [row[0] for row in rows]


def describe_table(connection: sqlite3.Connection, table: str) -> list[dict[str, Any]]:
    rows = connection.execute("PRAGMA table_info(%s)" % common.quote_sqlite(table)).fetchall()
    return [
        {
            "name": row[1],
            "type": row[2] or "",
            "notnull": bool(row[3]),
            "default": row[4],
            "pk": int(row[5]),
        }
        for row in rows
    ]


def blocking_objects(connection: sqlite3.Connection) -> list[str]:
    """视图/触发器会被迁移脚本直接拒绝，这里先查出来。"""
    rows = connection.execute(
        "SELECT type, name FROM sqlite_master WHERE type IN ('view', 'trigger') "
        "AND lower(substr(name, 1, 7)) <> 'sqlite_' ORDER BY type, name"
    ).fetchall()
    return ["%s:%s" % (kind, name) for kind, name in rows]


def notnull_violations(connection: sqlite3.Connection, table: str, columns: list[dict[str, Any]]) -> dict[str, int]:
    """一次聚合查出所有 NOT NULL 列的 NULL 数量，避免逐列扫表。"""
    targets = [column for column in columns if column["notnull"] and column["pk"] == 0]
    if not targets:
        return {}
    select = ", ".join(
        "SUM(CASE WHEN %s IS NULL THEN 1 ELSE 0 END)" % common.quote_sqlite(column["name"]) for column in targets
    )
    row = connection.execute("SELECT %s FROM %s" % (select, common.quote_sqlite(table))).fetchone()
    return {column["name"]: int(value or 0) for column, value in zip(targets, row) if int(value or 0)}


def max_text_lengths(
    connection: sqlite3.Connection, table: str, columns: list[dict[str, Any]]
) -> dict[str, int]:
    """一次聚合查出所有文本列的最长字符数（SQLite 的 LENGTH 对文本按字符计）。"""
    targets = [
        column
        for column in columns
        if column["type"] and any(common.parse_declared_type(column["type"]).startswith(item) for item in TEXT_TYPES)
    ]
    if not targets:
        return {}
    select = ", ".join(
        "MAX(LENGTH(%s))" % common.quote_sqlite(column["name"]) for column in targets
    )
    row = connection.execute("SELECT %s FROM %s" % (select, common.quote_sqlite(table))).fetchone()
    return {column["name"]: int(value or 0) for column, value in zip(targets, row)}


def numeric_outliers(connection: sqlite3.Connection, table: str, columns: list[dict[str, Any]]) -> dict[str, int]:
    """numeric 无小数位的列：脚本会映射成 DOUBLE，检查是否真的只有 0/1。"""
    targets = [
        column
        for column in columns
        if common.parse_declared_type(column["type"]).startswith(("NUMERIC", "DECIMAL"))
        and common.declared_scale(column["type"]) is None
    ]
    result: dict[str, int] = {}
    for column in targets:
        name = common.quote_sqlite(column["name"])
        count = connection.execute(
            "SELECT COUNT(*) FROM %s WHERE %s IS NOT NULL AND %s NOT IN (0, 1)" % (common.quote_sqlite(table), name, name)
        ).fetchone()[0]
        if count:
            result[column["name"]] = int(count)
    return result


def datetime_forms(
    connection: sqlite3.Connection, table: str, columns: list[dict[str, Any]], sample: int = 300
) -> dict[str, Any]:
    """抽样统计时间文本的时区偏移与小数位数。

    只对首尾各 sample 行取样：SQLite 的旧行是 +00:00（容器 TZ=UTC 时期）、
    新行是 +08:00，首尾取样正好覆盖两种形态；这里只出提醒，不需要精确全量。
    不用 SQL 直接量小数位，是因为偏移（+08:00）会被算进小数部分而误报。
    """
    result: dict[str, Any] = {}
    for column in columns:
        declared = common.parse_declared_type(column["type"])
        if "DATE" not in declared and "TIME" not in declared:
            continue
        name = common.quote_sqlite(column["name"])
        table_sql = common.quote_sqlite(table)
        values: list[Any] = []
        for direction in ("ASC", "DESC"):
            values.extend(
                row[0]
                for row in connection.execute(
                    "SELECT %s FROM %s WHERE %s IS NOT NULL AND %s <> '' ORDER BY rowid %s LIMIT ?"
                    % (name, table_sql, name, name, direction),
                    (sample,),
                )
            )
        offsets: set[str] = set()
        digits = 0
        for value in values:
            match = common.SQLITE_DATETIME_RE.match(value.strip()) if isinstance(value, str) else None
            if not match:
                continue
            fraction, offset = match.group(7), match.group(8)
            if offset:
                offsets.add(offset)
            if fraction:
                digits = max(digits, len(fraction))
        result[column["name"]] = {
            "offsets": sorted(offsets),
            "max_fraction_digits": digits,
            "sampled": len(values),
        }
    return result


def export_long_values(
    connection: sqlite3.Connection,
    items: list[dict[str, Any]],
    path: str,
) -> None:
    payload: dict[str, dict[str, list[dict[str, Any]]]] = {}
    for item in items:
        table, column, limit = item["table"], item["column"], item["limit"]
        rows = connection.execute(
            "SELECT rowid, %s FROM %s WHERE LENGTH(%s) > ? ORDER BY rowid"
            % (common.quote_sqlite(column), common.quote_sqlite(table), common.quote_sqlite(column)),
            (limit,),
        ).fetchall()
        payload.setdefault(table, {})[column] = [
            {"rowid": row[0], "chars": len(row[1]), "value": row[1]} for row in rows
        ]
    Path(path).write_text(json.dumps(payload, ensure_ascii=False, indent=1), encoding="utf-8")


def main() -> int:
    args = parse_args()
    connection = open_readonly(args.sqlite_path)
    report: dict[str, Any] = {
        "sqlite": str(Path(args.sqlite_path).resolve()),
        "checked_at": dt.datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
        "blocking": [],
        "warnings": [],
        "tables": {},
        "long_values": [],
    }
    try:
        tables = list_tables(connection)
        report["tables_count"] = len(tables)

        objects = blocking_objects(connection)
        if objects:
            report["blocking"].append({"check": "views_triggers", "detail": objects})

        for table in tables:
            columns = describe_table(connection, table)
            entry: dict[str, Any] = {}

            if connection.execute("PRAGMA foreign_key_list(%s)" % common.quote_sqlite(table)).fetchone():
                report["blocking"].append({"check": "foreign_key", "table": table})

            hidden = [
                row[1]
                for row in connection.execute("PRAGMA table_xinfo(%s)" % common.quote_sqlite(table)).fetchall()
                if len(row) > 6 and row[6] != 0
            ]
            if hidden:
                report["blocking"].append({"check": "generated_column", "table": table, "detail": hidden})

            ddl = (
                connection.execute("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", (table,)).fetchone()
                or [""]
            )[0] or ""
            if "CHECK" in ddl.upper():
                report["blocking"].append({"check": "check_constraint", "table": table})

            violations = notnull_violations(connection, table, columns)
            if violations:
                report["blocking"].append({"check": "not_null", "table": table, "detail": violations})

            lengths = max_text_lengths(connection, table, columns)
            for column in columns:
                limit = common.declared_length(column["type"])
                declared = common.parse_declared_type(column["type"])
                if not limit or not declared.startswith(("VARCHAR", "CHAR")):
                    continue
                actual = lengths.get(column["name"], 0)
                entry.setdefault("text_lengths", {})[column["name"]] = {"declared": limit, "actual_max": actual}
                if actual > limit:
                    item = {
                        "check": "varchar_overflow",
                        "table": table,
                        "column": column["name"],
                        "declared": limit,
                        "actual_max": actual,
                    }
                    report["blocking"].append(item)
                    report["long_values"].append({"table": table, "column": column["name"], "limit": limit})

            outliers = numeric_outliers(connection, table, columns)
            if outliers:
                report["warnings"].append({"check": "numeric_not_boolean", "table": table, "detail": outliers})

            forms = datetime_forms(connection, table, columns)
            if forms:
                offsets = {offset for item in forms.values() for offset in item["offsets"]}
                digits = max(item["max_fraction_digits"] for item in forms.values())
                entry["datetime"] = forms
                if len(offsets) > 1:
                    report["warnings"].append({"check": "mixed_timezone", "table": table, "detail": sorted(offsets)})
                if digits > 6:
                    report["warnings"].append(
                        {
                            "check": "nanosecond_datetime",
                            "table": table,
                            "detail": "抽样首尾行最长 %d 位小数，MySQL 只保留 6 位（写库按墙钟截断）" % digits,
                        }
                    )

            report["tables"][table] = entry

        if args.export_long and report["long_values"]:
            export_long_values(connection, report["long_values"], args.export_long)
            report["long_values_exported"] = args.export_long
    finally:
        connection.close()

    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        print("SQLite: %s（%d 张业务表）" % (report["sqlite"], report["tables_count"]))
        if report["blocking"]:
            print("\n阻断项（%d 条）：" % len(report["blocking"]))
            for item in report["blocking"]:
                print("  - %s" % json.dumps(item, ensure_ascii=False))
        else:
            print("\n没有阻断项。")
        if report["warnings"]:
            print("\n提醒项（%d 条）：" % len(report["warnings"]))
            for item in report["warnings"]:
                print("  - %s" % json.dumps(item, ensure_ascii=False))
        if report.get("long_values_exported"):
            print("\n超长字段原文已导出：%s" % report["long_values_exported"])
        print("\n结论：%s" % ("存在阻断项，迁移会失败或 MySQL 拒绝写入" if report["blocking"] else "可以迁移"))

    return 2 if report["blocking"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
