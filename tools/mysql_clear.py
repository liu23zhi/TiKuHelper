#!/usr/bin/env python3
"""清空目标 MySQL 库（先导结构备份，再 DROP 全部对象）。

典型用法：

    # 先看看会删什么
    python tools/mysql_clear.py --dry-run --mysql-user TikuHelper --mysql-database TikuHelper

    # 真正清空（默认把结构备份写到当前目录）
    python tools/mysql_clear.py --yes --backup-schema logs/mysql-schema-before-clear.sql

安全约定：
- 不加 --yes 不会删任何东西；
- 默认先写结构备份（仅结构，不含数据）；--backup-schema '' 可显式关闭；
- 只删对象，不删库本身（保留授权与库字符集设置）。
"""

from __future__ import annotations

import argparse
import datetime as dt
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import mysql_migration_common as common  # noqa: E402
from mysql_inspect import dump_schema  # noqa: E402


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="清空目标 MySQL 库（先导结构备份）")
    common.add_mysql_arguments(parser)
    parser.add_argument("--yes", action="store_true", help="确认执行删除；缺省只做预演")
    parser.add_argument("--dry-run", action="store_true", help="只打印将删除的对象，不做任何修改")
    parser.add_argument(
        "--backup-schema",
        default="mysql-schema-before-clear.sql",
        help="清空前导出结构备份的路径；传空字符串表示不备份",
    )
    return parser.parse_args()


def list_objects(cursor) -> list[tuple[str, str]]:
    cursor.execute(
        "SELECT table_name, table_type FROM information_schema.tables "
        "WHERE table_schema = DATABASE() ORDER BY table_name"
    )
    return [(row[0], row[1]) for row in cursor.fetchall()]


def main() -> int:
    args = parse_args()
    connection, _ = common.connect(args)
    try:
        with connection.cursor() as cursor:
            cursor.execute("SELECT VERSION(), DATABASE(), CURRENT_USER()")
            version, database, user = cursor.fetchone()
            print("目标: %s@%s (%s)" % (database, version, user))

            objects = list_objects(cursor)
            if not objects:
                print("库已经是空的，无需清理。")
                return 0
            print("将删除 %d 个对象：" % len(objects))
            for name, kind in objects:
                print("  %-8s %s" % ("TABLE" if kind == "BASE TABLE" else "VIEW", name))

            if args.dry_run:
                print("--dry-run：未做任何修改。")
                return 0
            if not args.yes:
                print("缺少 --yes，未做任何修改。确认无误后重新运行并加上 --yes。")
                return 1

            if args.backup_schema:
                path = args.backup_schema
                if Path(path).exists():
                    stem = Path(path).stem
                    suffix = Path(path).suffix or ".sql"
                    path = "%s-%s%s" % (stem, dt.datetime.now().strftime("%Y%m%d_%H%M%S"), suffix)
                count = dump_schema(cursor, path)
                print("结构备份已写入 %s（%d 张表）" % (path, count))

            cursor.execute("SET FOREIGN_KEY_CHECKS = 0")
            for name, kind in objects:
                keyword = "TABLE" if kind == "BASE TABLE" else "VIEW"
                cursor.execute("DROP %s IF EXISTS %s" % (keyword, common.quote_mysql(name)))
                print("  dropped %-6s %s" % (keyword, name))
            cursor.execute("SET FOREIGN_KEY_CHECKS = 1")

            remaining = list_objects(cursor)
            print("剩余对象数: %d" % len(remaining))
            return 0 if not remaining else 1
    finally:
        connection.close()


if __name__ == "__main__":
    raise SystemExit(main())
