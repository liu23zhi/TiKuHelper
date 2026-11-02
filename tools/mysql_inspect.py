#!/usr/bin/env python3
"""只读体检目标 MySQL/MariaDB，并可选导出结构备份。

典型用法：

    # 看版本、字符集、表清单与估算行数
    python tools/mysql_inspect.py --mysql-user TikuHelper --mysql-database TikuHelper

    # 精确行数 + 导出结构备份（迁移前留底）
    python tools/mysql_inspect.py --exact \\
      --dump-schema logs/mysql-schema-before-migrate.sql

密码从 MYSQL_PASSWORD 读取，或用 --mysql-password/交互输入。本脚本不会修改任何数据。
"""

from __future__ import annotations

import argparse
import datetime as dt
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mysql_migration_common as common  # noqa: E402


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="只读体检目标 MySQL/MariaDB（可选导出结构备份）")
    common.add_mysql_arguments(parser)
    parser.add_argument("--exact", action="store_true", help="逐表 COUNT(*) 精确行数（大表较慢）")
    parser.add_argument("--dump-schema", help="把 SHOW CREATE TABLE 结果写入该文件（含 DROP TABLE 前置语句）")
    return parser.parse_args()


def dump_schema(cursor, path: str) -> int:
    """导出结构备份：只含 CREATE TABLE，供清库后回滚/比对，不含任何数据。"""
    cursor.execute(
        "SELECT table_name FROM information_schema.tables "
        "WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY table_name"
    )
    names = [row[0] for row in cursor.fetchall()]
    lines = [
        "-- tikuAdapter 目标库结构备份（仅结构，不含数据）",
        "-- 生成时间 %s，表数 %d" % (dt.datetime.now().strftime("%Y-%m-%d %H:%M:%S"), len(names)),
        "SET NAMES utf8mb4;",
        "",
    ]
    for name in names:
        cursor.execute("SHOW CREATE TABLE %s" % common.quote_mysql(name))
        lines.append("DROP TABLE IF EXISTS %s;" % common.quote_mysql(name))
        lines.append(cursor.fetchone()[1] + ";")
        lines.append("")
    Path(path).write_text("\n".join(lines), encoding="utf-8")
    return len(names)


def main() -> int:
    args = parse_args()
    connection, _ = common.connect(args)
    try:
        with connection.cursor() as cursor:
            cursor.execute("SELECT VERSION(), @@character_set_database, @@collation_database, CURRENT_USER()")
            version, charset, collation, user = cursor.fetchone()
            print("服务器: %s" % version)
            print("库字符集: %s/%s  当前账号: %s" % (charset, collation, user))

            cursor.execute(
                "SELECT table_name, table_type, table_rows, engine FROM information_schema.tables "
                "WHERE table_schema = DATABASE() ORDER BY table_name"
            )
            rows = cursor.fetchall()
            print("对象数: %d" % len(rows))
            total = 0
            for name, kind, estimate, engine in rows:
                if args.exact and kind == "BASE TABLE":
                    cursor.execute("SELECT COUNT(*) FROM %s" % common.quote_mysql(name))
                    count = cursor.fetchone()[0]
                    total += int(count)
                    print("  %-34s %-10s %-8s %d 行" % (name, kind, engine, count))
                else:
                    estimate = int(estimate or 0)
                    total += estimate
                    print("  %-34s %-10s %-8s 约 %d 行" % (name, kind, engine, estimate))
            print("行数合计（%s）: %d" % ("精确" if args.exact else "估算", total))

            if args.dump_schema:
                count = dump_schema(cursor, args.dump_schema)
                print("结构备份已写入 %s（%d 张表）" % (args.dump_schema, count))
    finally:
        connection.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
