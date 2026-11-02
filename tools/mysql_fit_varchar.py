#!/usr/bin/env python3
"""按 MySQL 列声明长度截断超长文本，并可选把列类型改回声明的 varchar(n)。

为什么需要它（线上踩过）：SQLite 不校验 varchar(n)，历史数据里 billing_records.
question_text 最长 3937 字符，而实体声明 `type:varchar(512)`。迁移完成后，应用
启动的 AutoMigrate 会执行：

    ALTER TABLE `billing_records` MODIFY COLUMN `question_text` varchar(512)

在严格模式下直接报 `Error 1406 (22001): Data too long for column 'question_text'`，
容器反复重启。做法是先把超长值截断（默认同时把原文导出成 JSON 备份），再让
AutoMigrate 正常收窗列类型。

典型用法：

    # 只看会影响多少行
    python tools/mysql_fit_varchar.py --table billing_records --column question_text \\
      --dry-run --mysql-user TikuHelper --mysql-database TikuHelper

    # 截断 + 导出原文 + 恢复成 varchar(512)
    python tools/mysql_fit_varchar.py --table billing_records --column question_text \\
      --export logs/question_text-over512.json --alter --yes \\
      --mysql-user TikuHelper --mysql-database TikuHelper
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mysql_migration_common as common  # noqa: E402


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="按 MySQL 列声明长度截断超长文本")
    common.add_mysql_arguments(parser)
    parser.add_argument("--table", required=True, help="目标表名")
    parser.add_argument("--column", required=True, help="目标列名")
    parser.add_argument("--limit", type=int, help="截断长度（字符）；缺省取列的声明长度")
    parser.add_argument("--export", help="把被截断的原文导出到该 JSON 文件（强烈建议）")
    parser.add_argument("--alter", action="store_true", help="截断后把列类型改回 varchar(n)/char(n)")
    parser.add_argument("--yes", action="store_true", help="确认执行写入；缺省只做预演")
    parser.add_argument("--dry-run", action="store_true", help="只报告影响行数，不写任何东西")
    return parser.parse_args()


def normalize_limit(limit: int | None) -> int | None:
    """截断长度必须为正整数；None 表示由调用方从 information_schema 取。"""
    if limit is None:
        return None
    if limit < 1:
        raise ValueError("截断长度必须 ≥ 1")
    return limit


def build_modify_type(column_type: str, limit: int) -> str | None:
    """text 系列不做类型收窄（保留大字段能力）；varchar/char 按声明长度重建类型。"""
    normalized = common.parse_declared_type(column_type)
    if normalized.startswith("VARCHAR"):
        return "varchar(%d)" % limit
    if normalized.startswith("CHAR"):
        return "char(%d)" % limit
    return None


def primary_key_columns(cursor, table: str) -> list[str]:
    cursor.execute(
        "SELECT column_name FROM information_schema.key_column_usage "
        "WHERE table_schema = DATABASE() AND table_name = %s AND constraint_name = 'PRIMARY' "
        "ORDER BY ordinal_position",
        (table,),
    )
    return [row[0] for row in cursor.fetchall()]


def column_meta(cursor, table: str, column: str) -> dict[str, Any]:
    cursor.execute(
        "SELECT column_type, data_type, character_maximum_length, is_nullable "
        "FROM information_schema.columns WHERE table_schema = DATABASE() "
        "AND table_name = %s AND column_name = %s",
        (table, column),
    )
    row = cursor.fetchone()
    if not row:
        raise SystemExit("列不存在：%s.%s" % (table, column))
    return {
        "column_type": row[0],
        "data_type": row[1],
        "max_length": int(row[2]) if row[2] is not None else None,
        "nullable": str(row[3]).upper() == "YES",
    }


def main() -> int:
    args = parse_args()
    connection, _ = common.connect(args)
    try:
        with connection.cursor() as cursor:
            meta = column_meta(cursor, args.table, args.column)
            limit = normalize_limit(args.limit) or meta["max_length"]
            if limit is None:
                raise SystemExit(
                    "%s.%s 没有声明长度（%s），请显式给 --limit" % (args.table, args.column, meta["column_type"])
                )
            table_sql = common.quote_mysql(args.table)
            column_sql = common.quote_mysql(args.column)

            cursor.execute("SELECT COUNT(*) FROM %s WHERE CHAR_LENGTH(%s) > %%s" % (table_sql, column_sql), (limit,))
            affected = int(cursor.fetchone()[0])
            cursor.execute("SELECT COALESCE(MAX(CHAR_LENGTH(%s)), 0) FROM %s" % (column_sql, table_sql))
            current_max = int(cursor.fetchone()[0])
            print("列: %s.%s (%s) 声明长度=%s 当前最长=%d 超长行=%d"
                  % (args.table, args.column, meta["column_type"], limit, current_max, affected))

            if affected == 0:
                print("没有超长数据，不需要截断。")
            elif args.dry_run:
                print("--dry-run：未写入任何数据。")
                return 0
            elif not args.yes:
                print("缺少 --yes，未写入任何数据。")
                return 1
            else:
                keys = primary_key_columns(cursor, args.table)
                if args.export:
                    select_keys = ", ".join(common.quote_mysql(name) for name in keys) if keys else "NULL"
                    cursor.execute(
                        "SELECT %s, %s FROM %s WHERE CHAR_LENGTH(%s) > %%s ORDER BY 1"
                        % (select_keys, column_sql, table_sql, column_sql),
                        (limit,),
                    )
                    payload = []
                    for row in cursor.fetchall():
                        keys_values = row[:-1] if keys else []
                        value = row[-1]
                        payload.append(
                            {
                                "keys": dict(zip(keys, keys_values)) if keys else None,
                                "chars": len(value or ""),
                                "value": value,
                            }
                        )
                    Path(args.export).write_text(json.dumps(payload, ensure_ascii=False, indent=1), encoding="utf-8")
                    print("原文已导出：%s（%d 条）" % (args.export, len(payload)))

                cursor.execute(
                    "UPDATE %s SET %s = LEFT(%s, %%s) WHERE CHAR_LENGTH(%s) > %%s"
                    % (table_sql, column_sql, column_sql, column_sql),
                    (limit, limit),
                )
                print("已截断行数: %d" % cursor.rowcount)

            if args.alter:
                target_type = build_modify_type(meta["column_type"], limit)
                if target_type is None:
                    print("列类型是 %s（大字段），不自动收窄类型；如确需 varchar 请另外处理。"
                          % meta["column_type"])
                else:
                    null_sql = "NULL" if meta["nullable"] else "NOT NULL"
                    statement = "ALTER TABLE %s MODIFY COLUMN %s %s %s" % (table_sql, column_sql, target_type, null_sql)
                    if args.dry_run:
                        print("--dry-run：将执行 %s" % statement)
                    else:
                        try:
                            cursor.execute(statement)
                            print("已调整列类型: %s" % target_type)
                        except Exception as exc:  # 保留原始错误，便于判断是否仍有超长数据
                            raise SystemExit("调整列类型失败（先确认截断已完成）：%s" % exc) from exc

            cursor.execute("SELECT COALESCE(MAX(CHAR_LENGTH(%s)), 0) FROM %s" % (column_sql, table_sql))
            final_max = int(cursor.fetchone()[0])
            print("截断后最长=%d，限制=%d -> %s" % (final_max, limit, "OK" if final_max <= limit else "仍超长"))
            return 0 if final_max <= limit else 1
    finally:
        connection.close()


if __name__ == "__main__":
    raise SystemExit(main())
