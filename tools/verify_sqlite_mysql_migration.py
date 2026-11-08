#!/usr/bin/env python3
"""逐行/抽样校验 SQLite 与 MySQL 两侧数据是否一致。

迁移脚本自身只比对「行数」。行数相同但内容错位（时间精度、金额精度、编码、
截断）不会被发现，所以再跑一遍内容校验：

- 小表：按主键排序逐行比对每一列；
- 大表：全表聚合（行数、主键区间、文本长度合计）+ 按主键等距抽样逐行比对。

值归一化口径（与 migrate_sqlite_to_mysql.py 保持一致）：
- 时间：SOURCE 可能带 9 位纳秒与 ±HH:MM 偏移（+00:00/+08:00 混存），
  MySQL DATETIME 无时区，统一「保留墙钟、截断到微秒」后比较；
- 金额：按列声明的小数位四舍五入后比较（SQLite 里是 REAL，MySQL 里是 DECIMAL）；
- 大字段：二进制按 SHA-256 比较，避免把几 MB 内容塞进报告。

已知且可解释的差异需要用参数显式放行，否则一律算失败：
    --allow-truncation billing_records.question_text=512

退出码：0 一致，1 有差异，2 参数/连接错误。
"""

from __future__ import annotations

import argparse
import datetime as dt
import decimal
import hashlib
import json
import sqlite3
import sys
from decimal import Decimal
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))

import migrate_sqlite_to_mysql as migration  # noqa: E402
import mysql_migration_common as common  # noqa: E402

NULL = "NULL"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="校验 SQLite → MySQL 迁移后的数据内容")
    parser.add_argument("--sqlite-path", required=True, help="源 SQLite 数据库路径（只读）")
    common.add_mysql_arguments(parser)
    parser.add_argument("--full-compare-limit", type=int, default=50000, help="行数不超过该值的表逐行比对，默认 50000")
    parser.add_argument("--sample-size", type=int, default=5000, help="大表抽样行数，默认 5000")
    parser.add_argument(
        "--allow-truncation",
        action="append",
        default=[],
        metavar="TABLE.COLUMN=LIMIT",
        help="声明「该列已按 LIMIT 截断」，可重复；比对时两侧都截到 LIMIT",
    )
    parser.add_argument(
        "--allow-row-growth",
        action="append",
        default=[],
        metavar="TABLE",
        help="允许目标库该表比源库多行（应用启动迁移会写 system_settings 标记），可重复",
    )
    parser.add_argument(
        "--datetime-precision",
        type=int,
        choices=(0, 1, 2, 3, 4, 5, 6),
        help="时间列比较精度；缺省按目标列的实际精度（GORM AutoMigrate 会把 6 收窄成 3）",
    )
    parser.add_argument("--json", action="store_true", help="以 JSON 输出结果")
    parser.add_argument("--quiet", action="store_true", help="只输出结论与失败明细")
    return parser.parse_args()


def open_sqlite_readonly(path: str) -> sqlite3.Connection:
    target = Path(path)
    if not target.is_file():
        raise SystemExit("SQLite 文件不存在：%s" % path)
    connection = sqlite3.connect(target.resolve().as_uri() + "?mode=ro", uri=True)
    connection.execute("PRAGMA query_only=ON")
    return connection


def sqlite_tables(connection: sqlite3.Connection) -> list[str]:
    rows = connection.execute(
        "SELECT name FROM sqlite_master WHERE type = 'table' AND lower(substr(name, 1, 7)) <> 'sqlite_' "
        "ORDER BY name"
    ).fetchall()
    return [row[0] for row in rows]


def table_columns(connection: sqlite3.Connection, table: str) -> list[dict[str, Any]]:
    return [
        {"name": row[1], "type": row[2] or "", "pk": int(row[5])}
        for row in connection.execute("PRAGMA table_info(%s)" % common.quote_sqlite(table)).fetchall()
    ]


def parse_truncation_specs(specs: list[str]) -> dict[tuple[str, str], int]:
    result: dict[tuple[str, str], int] = {}
    for spec in specs:
        target, _, limit_text = spec.partition("=")
        table, _, column = target.partition(".")
        if not table or not column or not limit_text:
            raise SystemExit("--allow-truncation 格式应为 TABLE.COLUMN=LIMIT，收到 %r" % spec)
        result[(table.lower(), column.lower())] = int(limit_text)
    return result


def sample_stride(total: int, target: int) -> int:
    """等距抽样步长；目标行数不小于全表时返回 1（即全取）。"""
    if target <= 0 or total <= target:
        return 1
    return max(1, total // target)


def mysql_datetime_precisions(cursor, table: str) -> dict[str, int]:
    """目标表每个时间列的实际小数位精度（0~6）。

    为什么要读它：GORM 启动 AutoMigrate 会把迁移脚本建的 datetime(6) 收窄成
    实体声明的 datetime(3)，微秒尾数被丢弃。校验必须按「目标实际精度」比较，
    否则正确的迁移也会被判成差异。
    """
    cursor.execute(
        "SELECT column_name, datetime_precision FROM information_schema.columns "
        "WHERE table_schema = DATABASE() AND table_name = %s AND data_type IN ('datetime', 'timestamp')",
        (table,),
    )
    return {row[0]: int(row[1]) for row in cursor.fetchall() if row[1] is not None}


def column_precision(column: dict[str, Any], precisions: dict[str, int], override: int | None) -> int | None:
    """时间列的有效比较精度；非时间列返回 None。"""
    declared = common.parse_declared_type(column["type"])
    if "DATE" not in declared and "TIME" not in declared:
        return None
    if override is not None:
        return override
    precision = precisions.get(column["name"])
    if precision is None:
        return 6
    return min(6, precision)


def canonical_value(value: Any, declared: str, precision: int = 6) -> str:
    """把 SQLite/MySQL 两侧的值归一成可比较字符串。"""
    if value is None:
        return NULL
    normalized = common.parse_declared_type(declared)
    if "DATE" in normalized or "TIME" in normalized:
        parsed = common.parse_sqlite_datetime(value)
        if parsed is None:
            return "RAW:" + str(value).strip()
        text = parsed.strftime("%Y-%m-%d %H:%M:%S.%f")
        if precision >= 6:
            return text
        return text[:19] if precision <= 0 else text[: 20 + precision]
    scale = common.declared_scale(declared)
    if scale is not None:
        try:
            return str(Decimal(str(value)).quantize(Decimal(1).scaleb(-scale), rounding=decimal.ROUND_HALF_UP))
        except Exception:
            return str(value)
    if normalized.startswith("NUMERIC") and isinstance(value, float) and value.is_integer():
        return str(int(value))
    if isinstance(value, (bytes, memoryview)):
        return "sha256:" + hashlib.sha256(bytes(value)).hexdigest()
    return str(value)


def values_equal(column: dict[str, Any], left: str, right: str, precision: int | None) -> bool:
    """先比归一化字符串；时间列允许一个最小刻度的差异（截断与四舍五入的差别）。"""
    if left == right:
        return True
    if precision is None:
        return False
    left_time = common.parse_sqlite_datetime(left)
    right_time = common.parse_sqlite_datetime(right)
    if left_time is None or right_time is None:
        return False
    return abs(left_time - right_time) <= dt.timedelta(seconds=10 ** -precision)


def row_digest(values: list[str]) -> str:
    digest = hashlib.sha256()
    for item in values:
        digest.update(item.encode("utf-8"))
        digest.update(b"\x1f")
    return digest.hexdigest()


def canonical_row(
    row: Any,
    columns: list[dict[str, Any]],
    truncations: dict[tuple[str, str], int],
    table: str,
    precisions: dict[str, int],
    precision_override: int | None,
) -> list[str]:
    values = [
        canonical_value(value, column["type"], column_precision(column, precisions, precision_override) or 6)
        for value, column in zip(row, columns)
    ]
    for index, column in enumerate(columns):
        limit = truncations.get((table.lower(), column["name"].lower()))
        if limit is not None:
            values[index] = values[index][:limit]
    return values


def compare_rows(
    table: str,
    columns: list[dict[str, Any]],
    source_rows: list[tuple],
    target_rows: list[tuple],
    truncations: dict[tuple[str, str], int],
    report: dict[str, Any],
    precisions: dict[str, int] | None = None,
    precision_override: int | None = None,
) -> int:
    """逐行比对；返回差异行数，并把前几条明细写进 report。"""
    precisions = precisions or {}
    mismatches = 0
    for source_row, target_row in zip(source_rows, target_rows):
        left = canonical_row(source_row, columns, truncations, table, precisions, precision_override)
        right = canonical_row(target_row, columns, truncations, table, precisions, precision_override)
        if row_digest(left) == row_digest(right):
            continue
        column_diffs = [
            (column["name"], source_value, target_value)
            for column, source_value, target_value in zip(columns, left, right)
            if not values_equal(column, source_value, target_value, column_precision(column, precisions, precision_override))
        ]
        if not column_diffs:
            # 归一化字符串不同但每个列都在容差内（例如只差微秒/毫秒），视为一致。
            continue
        mismatches += 1
        if len(report.setdefault("differences", [])) < 20:
            report["differences"].append({"table": table, "rows": column_diffs[:6]})
    return mismatches


def compare_table(
    source: sqlite3.Connection,
    target_cursor,
    table: str,
    args: argparse.Namespace,
    truncations: dict[tuple[str, str], int],
    report: dict[str, Any],
) -> None:
    columns = table_columns(source, table)
    names = [column["name"] for column in columns]
    table_sql = common.quote_mysql(table)
    source_sql_table = common.quote_sqlite(table)

    source_count = int(source.execute("SELECT COUNT(*) FROM %s" % source_sql_table).fetchone()[0])
    target_cursor.execute("SELECT COUNT(*) FROM %s" % table_sql)
    target_count = int(target_cursor.fetchone()[0])

    precision_map = mysql_datetime_precisions(target_cursor, table)
    narrowed = {name: precision for name, precision in precision_map.items() if precision < 6}
    if narrowed:
        report.setdefault("precision_notes", []).append({"table": table, "columns": narrowed})

    primary = sorted([column for column in columns if column["pk"] > 0], key=lambda item: item["pk"])
    order_sql = ", ".join(common.quote_sqlite(column["name"]) for column in primary) or "rowid"
    target_order = ", ".join(common.quote_mysql(column["name"]) for column in primary) or "rowid"
    allowed_growth = {name.lower() for name in args.allow_row_growth}

    if source_count != target_count:
        if table.lower() in allowed_growth and target_count > source_count and len(primary) == 1:
            # 应用启动迁移会往 system_settings 之类的小表追加标记行，按主键逐行比对源行即可。
            report.setdefault("growth", []).append(
                {"table": table, "source": source_count, "target": target_count}
            )
            key_name = primary[0]["name"]
            source_rows = source.execute(
                "SELECT %s FROM %s ORDER BY %s"
                % (", ".join(common.quote_sqlite(name) for name in names), source_sql_table, common.quote_sqlite(key_name))
            ).fetchall()
            mismatches = 0
            for start in range(0, len(source_rows), 500):
                chunk = [row[0] for row in source_rows[start:start + 500]]
                target_cursor.execute(
                    "SELECT %s FROM %s WHERE %s IN (%s)"
                    % (
                        ", ".join(common.quote_mysql(name) for name in names),
                        table_sql,
                        common.quote_mysql(key_name),
                        ",".join(["%s"] * len(chunk)),
                    ),
                    chunk,
                )
                matched = {row[0]: row for row in target_cursor.fetchall()}
                pairs = [(row, matched[row[0]]) for row in source_rows[start:start + 500] if row[0] in matched]
                mismatches += compare_rows(
                    table,
                    columns,
                    [pair[0] for pair in pairs],
                    [pair[1] for pair in pairs],
                    truncations,
                    report,
                    precisions=precision_map,
                    precision_override=args.datetime_precision,
                )
                missing = len(chunk) - len(pairs)
                mismatches += missing
            report["tables"][table] = {
                "rows": source_count,
                "target_rows": target_count,
                "mode": "keyed",
                "status": "OK（目标多 %d 行，已按主键核对源行）" % (target_count - source_count)
                if mismatches == 0
                else "差异 %d 行" % mismatches,
                "mismatches": mismatches,
            }
            return
        report["tables"][table] = {"rows": source_count, "target_rows": target_count, "status": "行数不一致"}
        return

    if source_count <= args.full_compare_limit or len(primary) != 1:
        source_rows = source.execute(
            "SELECT %s FROM %s ORDER BY %s"
            % (", ".join(common.quote_sqlite(name) for name in names), source_sql_table, order_sql)
        ).fetchall()
        target_cursor.execute(
            "SELECT %s FROM %s ORDER BY %s"
            % (", ".join(common.quote_mysql(name) for name in names), table_sql, target_order)
        )
        target_rows = target_cursor.fetchall()
        mismatches = compare_rows(
            table,
            columns,
            source_rows,
            target_rows,
            truncations,
            report,
            precisions=precision_map,
            precision_override=args.datetime_precision,
        )
        report["tables"][table] = {
            "rows": source_count,
            "mode": "full",
            "status": "OK" if mismatches == 0 else "差异 %d 行" % mismatches,
            "mismatches": mismatches,
        }
        return

    # 大表：全表聚合 + 等距抽样
    text_columns = [column for column in columns if common.is_big_text_type(column["type"])]
    aggregates = ["COUNT(*)", "MIN(%s)" % common.quote_sqlite(primary[0]["name"]), "MAX(%s)" % common.quote_sqlite(primary[0]["name"])]
    target_aggregates = ["COUNT(*)", "MIN(%s)" % common.quote_mysql(primary[0]["name"]), "MAX(%s)" % common.quote_mysql(primary[0]["name"])]
    for column in text_columns:
        aggregates.append("COALESCE(SUM(LENGTH(%s)), 0)" % common.quote_sqlite(column["name"]))
        target_aggregates.append("COALESCE(SUM(CHAR_LENGTH(%s)), 0)" % common.quote_mysql(column["name"]))
    source_aggregate = [int(value) for value in source.execute("SELECT %s FROM %s" % (", ".join(aggregates), source_sql_table)).fetchone()]
    target_cursor.execute("SELECT %s FROM %s" % (", ".join(target_aggregates), table_sql))
    target_aggregate = [int(value) for value in target_cursor.fetchone()]

    stride = sample_stride(source_count, args.sample_size)
    key_sql = common.quote_sqlite(primary[0]["name"])
    sampled_keys = [
        row[0] for row in source.execute("SELECT %s FROM %s WHERE %s %% ? = 0 ORDER BY 1" % (key_sql, source_sql_table, key_sql), (stride,))
    ]
    mismatches = 0
    for start in range(0, len(sampled_keys), 500):
        chunk = sampled_keys[start:start + 500]
        placeholders = ",".join(["?"] * len(chunk))
        source_rows = source.execute(
            "SELECT %s FROM %s WHERE %s IN (%s)"
            % (", ".join(common.quote_sqlite(name) for name in names), source_sql_table, key_sql, placeholders),
            chunk,
        ).fetchall()
        by_key = {row[0]: row for row in source_rows}
        target_cursor.execute(
            "SELECT %s FROM %s WHERE %s IN (%s)"
            % (
                ", ".join(common.quote_mysql(name) for name in names),
                table_sql,
                common.quote_mysql(primary[0]["name"]),
                ",".join(["%s"] * len(chunk)),
            ),
            chunk,
        )
        target_rows = [row for row in target_cursor.fetchall() if row[0] in by_key]
        source_ordered = [by_key[row[0]] for row in target_rows]
        mismatches += compare_rows(
            table,
            columns,
            source_ordered,
            target_rows,
            truncations,
            report,
            precisions=precision_map,
            precision_override=args.datetime_precision,
        )

    aggregate_ok = source_aggregate == target_aggregate
    report["tables"][table] = {
        "rows": source_count,
        "mode": "sample",
        "sampled": len(sampled_keys),
        "aggregate_ok": aggregate_ok,
        "status": "OK" if aggregate_ok and mismatches == 0 else "聚合或抽样不一致",
        "mismatches": mismatches,
    }
    if not aggregate_ok:
        report["differences"].append(
            {"table": table, "rows": [("aggregate", source_aggregate, target_aggregate)]}
        )


def main() -> int:
    args = parse_args()
    truncations = parse_truncation_specs(args.allow_truncation)
    source = open_sqlite_readonly(args.sqlite_path)
    target = None
    report: dict[str, Any] = {"tables": {}, "differences": []}
    failures: list[str] = []
    try:
        tables = sqlite_tables(source)
        migration.validate_access_token_quotas(source, tables)
        target, _ = common.connect(args)
        for table in tables:
            compare_table(source, target.cursor(), table, args, truncations, report)
            entry = report["tables"][table]
            if not entry["status"].startswith("OK"):
                failures.append("%s: %s" % (table, entry["status"]))
            if not args.quiet:
                detail = "（%s）" % entry["mode"] if "mode" in entry else ""
                print("%-30s %8d 行 %-8s %s" % (table, entry["rows"], detail, entry["status"]))
    except RuntimeError as exc:
        print(f"验证失败：{exc}", file=sys.stderr)
        return 1
    finally:
        source.close()
        if target is not None:
            target.close()

    if args.json:
        print(json.dumps({"report": report, "failures": failures}, ensure_ascii=False, indent=2))
    else:
        notes = report.get("precision_notes") or []
        if notes:
            print("\n时间列精度提示（%d 张表）：目标列低于 6 位，比较按实际精度与最小刻度容差进行" % len(notes))
            for item in notes[:5]:
                print("  %s: %s" % (item["table"], item["columns"]))
        for item in report["differences"][:10]:
            print("  差异 %s: %s" % (item["table"], item["rows"]))
        print("\n结论：%s" % ("全部一致" if not failures else "不一致 " + ", ".join(failures)))
    return 0 if not failures else 1


if __name__ == "__main__":
    raise SystemExit(main())
