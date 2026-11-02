#!/usr/bin/env python3
"""比较两份 SHOW CREATE TABLE 结构转储，输出「列 / 键 / 表属性」级差异。

典型用法：

    # 迁移前后对比（before 是清库前导出的备份）
    python tools/mysql_compare_schema.py \\
      --before mysql-schema-before-migrate.sql --after mysql-schema-after.sql

    # 直接把当前库当 after
    python tools/mysql_compare_schema.py --before before.sql --live \\
      --mysql-user TikuHelper --mysql-database TikuHelper

比较口径（重要，避免误报）：
- 忽略 AUTO_INCREMENT 的具体数值（同一个表复制后必然不同）；
- 忽略列/键的书写顺序与多余空白（只比集合）；
- 不忽略列类型、NULL 性、默认值、索引前缀长度与索引唯一性。

退出码：0 完全一致，1 存在差异，2 参数/解析错误。可直接用于脚本化校验。
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

CREATE_TABLE_RE = re.compile(r"CREATE\s+TABLE\s+`([^`]+)`\s*\(", re.IGNORECASE)
KEY_PREFIX_RE = re.compile(
    r"^(PRIMARY\s+KEY|UNIQUE\s+KEY|UNIQUE\s+INDEX|KEY|INDEX|FULLTEXT\s+KEY|SPATIAL\s+KEY)",
    re.IGNORECASE,
)
COLUMN_PREFIX_RE = re.compile(r"^`([^`]+)`")


def scan_parenthesized(text: str, start: int) -> tuple[str, int]:
    """从 text[start] == '(' 开始返回 (括号内文本, 右括号后一位)；跳过引号内的括号。"""
    if text[start] != "(":
        raise ValueError("起点不是左括号")
    depth = 0
    index = start
    quote: str | None = None
    while index < len(text):
        char = text[index]
        if quote is not None:
            if char == "\\" and quote == "'":
                index += 2
                continue
            if char == quote:
                quote = None
            index += 1
            continue
        if char in ("`", "'", '"'):
            quote = char
        elif char == "(":
            depth += 1
        elif char == ")":
            depth -= 1
            if depth == 0:
                return text[start + 1:index], index + 1
        index += 1
    raise ValueError("括号不匹配，转储可能被截断")


def split_definition_list(body: str) -> list[str]:
    """按顶层逗号切分建表定义，`decimal(18,6)`、`KEY (a, b)` 内的逗号不算。"""
    parts: list[str] = []
    depth = 0
    quote: str | None = None
    current: list[str] = []
    index = 0
    while index < len(body):
        char = body[index]
        if quote is not None:
            current.append(char)
            if char == "\\" and quote == "'" and index + 1 < len(body):
                current.append(body[index + 1])
                index += 2
                continue
            if char == quote:
                quote = None
            index += 1
            continue
        if char in ("`", "'", '"'):
            quote = char
            current.append(char)
        elif char == "(":
            depth += 1
            current.append(char)
        elif char == ")":
            depth -= 1
            current.append(char)
        elif char == "," and depth == 0:
            parts.append("".join(current))
            current = []
        else:
            current.append(char)
        index += 1
    if "".join(current).strip():
        parts.append("".join(current))
    return [re.sub(r"\s+", " ", part).strip() for part in parts if part.strip()]


def normalize_table_options(options: str) -> str:
    """表属性归一化：去掉 AUTO_INCREMENT 数值与多余空白。"""
    cleaned = re.sub(r"AUTO_INCREMENT\s*=\s*\d+", "AUTO_INCREMENT=*", options)
    cleaned = cleaned.split(";")[0]
    return re.sub(r"\s+", " ", cleaned).strip()


def classify_definition(definition: str) -> str:
    if COLUMN_PREFIX_RE.match(definition):
        return "column"
    if KEY_PREFIX_RE.match(definition):
        return "key"
    return "other"


def parse_schema_dump(text: str) -> dict[str, dict[str, Any]]:
    """解析 SHOW CREATE TABLE 转储 → {表名: {columns, keys, other, options}}。"""
    tables: dict[str, dict[str, Any]] = {}
    for match in CREATE_TABLE_RE.finditer(text):
        name = match.group(1)
        body, end = scan_parenthesized(text, match.end() - 1)
        options_end = text.find(";", end)
        options = text[end:] if options_end < 0 else text[end:options_end]
        bucket: dict[str, set[str]] = {"column": set(), "key": set(), "other": set()}
        for definition in split_definition_list(body):
            bucket[classify_definition(definition)].add(definition)
        tables[name] = {
            "columns": bucket["column"],
            "keys": bucket["key"],
            "other": bucket["other"],
            "options": normalize_table_options(options),
        }
    if not tables:
        raise ValueError("转储里没有找到 CREATE TABLE，确认输入的是 SHOW CREATE TABLE 输出")
    return tables


def _index_by_name(definitions: set[str], pattern: re.Pattern[str]) -> dict[str, set[str]]:
    grouped: dict[str, set[str]] = {}
    for definition in definitions:
        match = pattern.match(definition)
        key = match.group(1).lower() if match else definition.lower()
        grouped.setdefault(key, set()).add(definition)
    return grouped


def diff_schema_dumps(before: dict[str, dict[str, Any]], after: dict[str, dict[str, Any]]) -> list[dict[str, str]]:
    """返回差异列表，每条含 table/kind/detail。空列表表示结构一致。"""
    differences: list[dict[str, str]] = []
    for name in sorted(set(before) - set(after)):
        differences.append({"table": name, "kind": "table_dropped", "detail": "只在 before 中存在"})
    for name in sorted(set(after) - set(before)):
        differences.append({"table": name, "kind": "table_added", "detail": "只在 after 中存在"})

    for name in sorted(set(before) & set(after)):
        old, new = before[name], after[name]
        for kind in ("columns", "keys"):
            grouped_old = _index_by_name(old[kind], COLUMN_PREFIX_RE)
            grouped_new = _index_by_name(new[kind], COLUMN_PREFIX_RE)
            for item in sorted(set(grouped_old) - set(grouped_new)):
                differences.append({"table": name, "kind": "%s_removed" % kind[:-1], "detail": item})
            for item in sorted(set(grouped_new) - set(grouped_old)):
                differences.append({"table": name, "kind": "%s_added" % kind[:-1], "detail": item})
            for item in sorted(set(grouped_old) & set(grouped_new)):
                if grouped_old[item] != grouped_new[item]:
                    differences.append(
                        {
                            "table": name,
                            "kind": "%s_changed" % kind[:-1],
                            "detail": "%s: %s -> %s"
                            % (
                                item,
                                " | ".join(sorted(grouped_old[item])),
                                " | ".join(sorted(grouped_new[item])),
                            ),
                        }
                    )
        if old["other"] != new["other"]:
            differences.append(
                {
                    "table": name,
                    "kind": "other_changed",
                    "detail": "%s -> %s" % (sorted(old["other"]), sorted(new["other"])),
                }
            )
        if old["options"] != new["options"]:
            differences.append(
                {"table": name, "kind": "options_changed", "detail": "%s -> %s" % (old["options"], new["options"])}
            )
    return differences


def live_schema_dump(args: argparse.Namespace) -> str:
    connection, _ = common.connect(args)
    try:
        with connection.cursor() as cursor:
            cursor.execute(
                "SELECT table_name FROM information_schema.tables "
                "WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY table_name"
            )
            lines = []
            for (name,) in cursor.fetchall():
                cursor.execute("SHOW CREATE TABLE %s" % common.quote_mysql(name))
                lines.append(cursor.fetchone()[1] + ";")
            return "\n\n".join(lines)
    finally:
        connection.close()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="比较两份 SHOW CREATE TABLE 转储")
    parser.add_argument("--before", required=True, help="对比基线转储文件")
    parser.add_argument("--after", help="对比目标转储文件")
    parser.add_argument("--live", action="store_true", help="用数据库当前结构作为 after")
    common.add_mysql_arguments(parser)
    parser.add_argument("--json", action="store_true", help="以 JSON 输出差异")
    parser.add_argument("--quiet", action="store_true", help="只输出结论，不逐条列出")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if not args.after and not args.live:
        raise SystemExit("请提供 --after 文件，或加 --live 用当前库结构")
    before_text = Path(args.before).read_text(encoding="utf-8")
    after_text = Path(args.after).read_text(encoding="utf-8") if args.after else live_schema_dump(args)
    try:
        before = parse_schema_dump(before_text)
        after = parse_schema_dump(after_text)
    except ValueError as exc:
        print("解析失败：%s" % exc, file=sys.stderr)
        return 2

    differences = diff_schema_dumps(before, after)
    if args.json:
        print(json.dumps({"tables": {"before": len(before), "after": len(after)}, "differences": differences},
                         ensure_ascii=False, indent=2))
    elif differences:
        print("发现 %d 处结构差异（表数 before=%d after=%d）：" % (len(differences), len(before), len(after)))
        if not args.quiet:
            for item in differences:
                print("  [%s] %s: %s" % (item["kind"], item["table"], item["detail"]))
    else:
        print("结构一致（表数 %d）；已忽略 AUTO_INCREMENT 数值与列/键顺序。" % len(before))
    return 1 if differences else 0


if __name__ == "__main__":
    raise SystemExit(main())
