#!/usr/bin/env python3
"""tikuAdapter SQLite → MySQL 迁移工具组的公共部分。

只放「多个脚本都要用」的东西：连接参数、密码来源、标识符引用、声明类型解析。
不在这里做任何业务判断，避免把一次性事故结论写死成规则。

密码来源优先级：
1. 显式 --mysql-password（会告警：可能进入 shell 历史/进程列表）；
2. 环境变量 MYSQL_PASSWORD；
3. 交互式询问。
"""

from __future__ import annotations

import argparse
import getpass
import importlib
import os
import re
import sys
from typing import Any

DEPENDENCY = "PyMySQL>=1.1.1,<2"
DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 3306


def load_pymysql(auto_install: bool = False):
    """导入 PyMySQL；--auto-install-deps 时允许用当前解释器 pip 安装。"""
    try:
        return importlib.import_module("pymysql")
    except ImportError:
        if not auto_install:
            raise SystemExit(
                "缺少 PyMySQL。请先执行 `python -m pip install PyMySQL`，"
                "或重新运行并添加 --auto-install-deps。"
            )
        print("正在安装缺少的依赖：", DEPENDENCY)
        import subprocess

        subprocess.check_call([sys.executable, "-m", "pip", "install", DEPENDENCY])
        try:
            return importlib.import_module("pymysql")
        except ImportError as exc:  # pragma: no cover - 依赖装失败才会走到
            raise SystemExit("PyMySQL 安装完成但仍无法导入，请检查 Python 环境。") from exc


def add_mysql_arguments(parser: argparse.ArgumentParser) -> argparse.ArgumentParser:
    """给脚本加上与 migrate_sqlite_to_mysql.py 一致的目标库参数。"""
    parser.add_argument("--mysql-host", default=DEFAULT_HOST, help="MySQL 主机，默认 127.0.0.1")
    parser.add_argument("--mysql-port", type=int, default=DEFAULT_PORT, help="MySQL 端口，默认 3306")
    parser.add_argument("--mysql-user", default=os.environ.get("MYSQL_USER"), help="用户名，默认读 MYSQL_USER")
    parser.add_argument(
        "--mysql-password",
        default=None,
        help="密码；建议改用环境变量 MYSQL_PASSWORD（命令行密码可能泄露）",
    )
    parser.add_argument(
        "--mysql-database",
        default=os.environ.get("MYSQL_DATABASE"),
        help="数据库名，默认读 MYSQL_DATABASE",
    )
    parser.add_argument("--auto-install-deps", action="store_true", help="缺少 PyMySQL 时自动 pip 安装")
    return parser


def resolve_password(value: str | None) -> str:
    if value is not None:
        print("警告：连接参数里带了密码，命令行可能被 shell 历史或进程列表记录。", file=sys.stderr)
        return value
    env_password = os.environ.get("MYSQL_PASSWORD")
    if env_password is not None:
        return env_password
    return getpass.getpass("MySQL 密码：")


def require_target(args: argparse.Namespace) -> dict[str, Any]:
    """校验并返回连接参数；缺少用户/库名时直接报错，不做猜测。"""
    user = args.mysql_user
    database = args.mysql_database
    if not user or not database:
        raise SystemExit("请提供 --mysql-user 与 --mysql-database（或设置 MYSQL_USER / MYSQL_DATABASE）")
    return {
        "host": args.mysql_host,
        "port": args.mysql_port,
        "user": user,
        "password": resolve_password(args.mysql_password),
        "database": database,
    }


def connect(args: argparse.Namespace, *, read_timeout: int = 600):
    """按参数连库，返回 (connection, pymysql)。会话时区固定 +08:00。"""
    pymysql = load_pymysql(getattr(args, "auto_install_deps", False))
    options = require_target(args)
    connection = pymysql.connect(
        **options,
        charset="utf8mb4",
        autocommit=True,
        connect_timeout=15,
        read_timeout=read_timeout,
    )
    with connection.cursor() as cursor:
        cursor.execute("SET SESSION time_zone = '+08:00'")
    return connection, pymysql


def quote_mysql(identifier: str) -> str:
    """MySQL 标识符引用（保留字 key/type/value 等必须加反引号）。"""
    if "\x00" in identifier:
        raise ValueError("标识符包含 NUL 字符")
    return "`" + identifier.replace("`", "``") + "`"


def quote_sqlite(identifier: str) -> str:
    return '"' + identifier.replace('"', '""') + '"'


def parse_declared_type(declared: str | None) -> str:
    """归一化建表语句里的声明类型：去多余空白、转大写。"""
    return re.sub(r"\s+", " ", (declared or "").strip()).upper()


def declared_length(declared: str | None) -> int | None:
    """取 varchar(n)/char(n)/binary(n) 的长度；没有声明长度返回 None。"""
    match = re.search(r"\(\s*(\d+)\s*\)", parse_declared_type(declared))
    return int(match.group(1)) if match else None


def declared_scale(declared: str | None) -> int | None:
    """取 decimal(p,s)/numeric(p,s) 的小数位。"""
    normalized = parse_declared_type(declared)
    if not (normalized.startswith("DECIMAL") or normalized.startswith("NUMERIC")):
        return None
    match = re.search(r"\(\s*\d+\s*,\s*(\d+)\s*\)", normalized)
    return int(match.group(1)) if match else None


def is_big_text_type(declared: str | None) -> bool:
    """text/blob 系列（MySQL 侧无法做普通唯一索引，索引要带前缀）。"""
    normalized = parse_declared_type(declared)
    return "TEXT" in normalized or "CLOB" in normalized or "BLOB" in normalized


SQLITE_DATETIME_RE = re.compile(
    r"^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?"
    r"(?:([+-]\d{2}:?\d{2})|Z)?$"
)


def parse_sqlite_datetime(value: Any) -> "datetime | None":
    """解析 SQLite 里的时间文本，返回「去掉时区后的墙钟时间」。

    为什么不用 datetime.fromisoformat：GORM/sqlite 会写出 9 位纳秒
    （如 2026-10-31 22:32:17.786635322+08:00），而 fromisoformat 在 3.11 之前
    只接受 3/6 位小数。这里统一按「保留墙钟、截断到微秒」处理，与
    migrate_sqlite_to_mysql.py 的既有约定一致（MySQL DATETIME 没有时区，
    应用按 time.Local=Asia/Shanghai 解释，历史的 +00:00/+08:00 混存只在展示上
    体现为原始写入时的墙钟）。
    """
    import datetime as _dt

    if isinstance(value, _dt.datetime):
        return value.replace(tzinfo=None, microsecond=value.microsecond)
    if not isinstance(value, str):
        return None
    match = SQLITE_DATETIME_RE.match(value.strip())
    if not match:
        return None
    year, month, day, hour, minute, second, fraction, _offset = match.groups()
    micro = int((fraction or "").ljust(6, "0")[:6]) if fraction else 0
    try:
        return _dt.datetime(
            int(year), int(month), int(day), int(hour), int(minute), int(second), micro
        )
    except ValueError:
        return None

