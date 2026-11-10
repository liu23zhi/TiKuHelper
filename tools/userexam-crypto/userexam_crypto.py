"""Encrypt or decrypt a UserExamInfo SQLite database copy.

This is the administrator-only offline tool. It never runs on the server: the
TIKU service deliberately has no SQLite3MC code and no copy of the cipher key.

The key is NOT stored in this file. It is supplied at run time through
``USEREXAM_PASSWORD`` or a ``password.txt`` file next to the script / inside the
PyInstaller bundle (see ``_load_password``). This repository is public, so the
key must never be committed here.
"""

from dataclasses import dataclass
import hashlib
import importlib
import os
from pathlib import Path
import queue
import shutil
import sqlite3
import struct
import sys
import tempfile
import threading


SQLITE_HEADER = b"SQLite format 3\0"
SIDECAR_SUFFIXES = ("-journal", "-wal", "-shm")

# Runtime key sources, in priority order:
#   1. USEREXAM_PASSWORD environment variable
#   2. password.txt next to the script (or in the PyInstaller bundle root)
#   3. password.txt in the current working directory
PASSWORD_ENV = "USEREXAM_PASSWORD"
PASSWORD_FILENAME = "password.txt"


TOOL_VERSION = "1.1.0"


class UserExamError(Exception):
    """An expected, user-facing database conversion error."""


@dataclass(frozen=True)
class ConversionResult:
    operation: str
    output_path: Path


def _resource_root():
    """Return the directory that holds ``_vendor``.

    PyInstaller one-file builds unpack data into ``sys._MEIPASS`` and leave
    ``__file__`` pointing at a path inside the archive, so a plain
    ``Path(__file__).parent`` lookup silently misses the bundled runtime.
    """
    bundled = getattr(sys, "_MEIPASS", None)
    if bundled:
        return Path(bundled)
    return Path(__file__).resolve().parent


def _password_search_dirs():
    """Directories searched for ``password.txt``, in priority order.

    The exe's own directory comes first: in a one-file build ``_MEIPASS`` points
    at a throw-away extraction directory, so ``password.txt`` placed next to the
    exe — which is what the README tells operators to do — would otherwise never
    be found.
    """
    directories = []
    if getattr(sys, "frozen", False):
        directories.append(Path(sys.executable).resolve().parent)
    directories.append(_resource_root())
    directories.append(Path.cwd())

    unique = []
    for directory in directories:
        if directory not in unique:
            unique.append(directory)
    return unique


def _load_password():
    """Read the cipher key from the environment or a local password file.

    Raises ``UserExamError`` with an operator-readable message when the key is
    missing. The key is never echoed back, never logged and never written to
    disk by this tool.
    """
    configured = os.environ.get(PASSWORD_ENV, "").strip()
    if configured:
        return configured

    for directory in _password_search_dirs():
        candidate = directory / PASSWORD_FILENAME
        try:
            if not candidate.is_file():
                continue
            value = candidate.read_text(encoding="utf-8").strip()
        except OSError:
            continue
        if value:
            return value

    raise UserExamError(
        f"未配置数据库口令。请设置环境变量 {PASSWORD_ENV}，"
        f"或把口令写入 exe 同目录的 {PASSWORD_FILENAME}（该文件不得提交到仓库）。"
    )


def infer_operation(source_path):
    """Return ``encrypt`` for plain SQLite files and ``decrypt`` otherwise."""
    source = Path(source_path)
    try:
        with source.open("rb") as database:
            header = database.read(len(SQLITE_HEADER))
    except OSError as exc:
        raise UserExamError("无法读取所选数据库文件。") from exc

    if len(header) < len(SQLITE_HEADER):
        raise UserExamError("所选文件过小，不是受支持的 SQLite 数据库。")
    return "encrypt" if header == SQLITE_HEADER else "decrypt"


def _load_backend():
    vendor = _resource_root() / "_vendor"
    if vendor.is_dir() and str(vendor) not in sys.path:
        sys.path.insert(0, str(vendor))
    try:
        backend = importlib.import_module("apsw")
    except (ImportError, OSError):
        raise UserExamError(
            "无法加载 SQLite3MC。随附运行库需要 64 位 CPython 3.11；"
            "请保留脚本同目录的 _vendor 文件夹。其他 Python 版本请按 README 安装依赖。"
        ) from None
    if not getattr(backend, "mc_version", None):
        raise UserExamError("当前加载的是普通 APSW，缺少加密支持。请使用随附的 SQLite3MC 运行库。")
    return backend


def _configure_cipher(connection):
    connection.pragma("cipher", "aes256cbc")
    connection.pragma("legacy", 0)
    connection.pragma("kdf_iter", 4001)


def _encrypted_readonly(path):
    backend = _load_backend()
    uri = Path(path).resolve().as_uri() + "?immutable=1"
    connection = backend.Connection(
        uri, flags=backend.SQLITE_OPEN_READONLY | backend.SQLITE_OPEN_URI
    )
    try:
        _configure_cipher(connection)
        connection.pragma("key", _load_password())
        connection.pragma("query_only", True)
        return connection
    except Exception:
        connection.close()
        raise


def _sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.digest()


def _sqlite_readonly(path):
    uri = Path(path).resolve().as_uri() + "?mode=ro&immutable=1"
    return sqlite3.connect(uri, uri=True)


def _quoted_identifier(identifier):
    return '"' + identifier.replace('"', '""') + '"'


def _encode_value(value):
    if value is None:
        tag, raw = b"n", b""
    elif isinstance(value, bool):
        tag, raw = b"i", b"1" if value else b"0"
    elif isinstance(value, int):
        tag, raw = b"i", str(value).encode("ascii")
    elif isinstance(value, float):
        tag, raw = b"f", struct.pack(">d", value)
    elif isinstance(value, bytes):
        tag, raw = b"b", value
    elif isinstance(value, str):
        tag, raw = b"t", value.encode("utf-8", "surrogatepass")
    else:
        raise UserExamError("数据库包含无法验证的数据类型。")
    return tag + len(raw).to_bytes(8, "big") + raw


def _snapshot(connection):
    integrity = list(connection.execute("PRAGMA integrity_check"))
    if integrity != [("ok",)]:
        raise UserExamError("数据库完整性检查未通过。")

    catalog = list(connection.execute(
        "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name"
    ))
    tables = []
    for kind, name, _, _ in catalog:
        if kind != "table":
            continue
        rows = connection.execute(f"SELECT * FROM {_quoted_identifier(name)}")
        row_count = 0
        row_sum = 0
        row_xor = 0
        for row in rows:
            row_digest = hashlib.sha256(b"".join(_encode_value(value) for value in row)).digest()
            value = int.from_bytes(row_digest, "big")
            row_sum = (row_sum + value) & ((1 << 256) - 1)
            row_xor ^= value
            row_count += 1
        tables.append((name, row_count, row_sum, row_xor))
    return catalog, tables


def _source_snapshot(path, operation):
    if operation == "encrypt":
        connection = _sqlite_readonly(path)
    else:
        connection = _encrypted_readonly(path)
    try:
        return _snapshot(connection)
    finally:
        connection.close()


def _output_occupied(path):
    return any(os.path.lexists(str(path) + suffix) for suffix in ("",) + SIDECAR_SUFFIXES)


def _output_path(source, operation):
    label = "encrypted" if operation == "encrypt" else "decrypted"
    suffix = source.suffix or ".db"
    stem = source.stem
    candidate = source.with_name(f"{stem}.{label}{suffix}")
    index = 1
    while _output_occupied(candidate):
        candidate = source.with_name(f"{stem}.{label} ({index}){suffix}")
        index += 1
    return candidate


def _reject_active_journal(source):
    for suffix in ("-wal", "-journal"):
        sidecar = Path(str(source) + suffix)
        if not sidecar.exists() or not sidecar.stat().st_size:
            continue
        if suffix == "-journal":
            with sidecar.open("rb") as journal:
                # PERSIST mode leaves a cold journal with a zeroed header.
                if journal.read(8) == bytes(8):
                    continue
        raise UserExamError("数据库仍有未合并的事务文件，请先关闭正在使用它的程序后重试。")


def _cleanup_staging(path):
    if path is None:
        return
    for suffix in ("",) + SIDECAR_SUFFIXES:
        try:
            Path(str(path) + suffix).unlink(missing_ok=True)
        except OSError:
            pass


def transform_database(source_path):
    """Create and validate an encrypted/decrypted sibling copy of a database."""
    try:
        source = Path(source_path).resolve(strict=True)
    except OSError:
        raise UserExamError("所选数据库文件不存在或无法访问。") from None
    if not source.is_file():
        raise UserExamError("所选路径不是数据库文件。")

    staging = None
    published = False
    try:
        operation = infer_operation(source)
        _reject_active_journal(source)
        _load_backend()
        before_hash = _sha256(source)
        try:
            source_snapshot = _source_snapshot(source, operation)
        except UserExamError:
            raise
        except Exception:
            if operation == "decrypt":
                raise UserExamError("无法解密：文件损坏，或口令、加密参数不属于本工具支持的配置。") from None
            raise UserExamError("所选文件不是有效且可读的 SQLite 数据库。") from None

        target = _output_path(source, operation)
        descriptor, staging_name = tempfile.mkstemp(
            prefix=f".{source.stem}.userexam-", suffix=".partial", dir=source.parent
        )
        staging = Path(staging_name)
        os.close(descriptor)
        shutil.copyfile(source, staging)
        _reject_active_journal(source)
        if _sha256(staging) != before_hash:
            raise UserExamError("创建临时副本时数据库发生变化，请关闭正在使用它的程序后重试。")

        backend = _load_backend()
        connection = backend.Connection(
            str(staging), flags=backend.SQLITE_OPEN_READWRITE
        )
        try:
            _configure_cipher(connection)
            if operation == "decrypt":
                connection.pragma("key", _load_password())

            staged_snapshot = _snapshot(connection)
            if staged_snapshot != source_snapshot:
                raise UserExamError("临时副本内容与源数据库不一致，已取消输出。")

            # Rekey requires rollback journaling, including for a closed WAL database.
            if str(connection.pragma("journal_mode", "delete")).lower() != "delete":
                raise UserExamError("无法将临时副本切换为可转换的事务模式。")
            if operation == "decrypt":
                connection.pragma("rekey", "")
            else:
                connection.pragma("rekey", _load_password())
        finally:
            connection.close()

        output_format = "decrypt" if operation == "encrypt" else "encrypt"
        verified_snapshot = _source_snapshot(staging, output_format)
        if verified_snapshot != source_snapshot:
            raise UserExamError("转换后校验不一致，未发布输出文件。")
        _reject_active_journal(source)
        if _sha256(source) != before_hash:
            raise UserExamError("源数据库在操作期间发生变化，未发布输出文件。")

        if _output_occupied(target):
            target = _output_path(source, operation)
        if os.name == "nt":
            os.rename(staging, target)
        else:
            # link() publishes without replacing an existing file on POSIX.
            os.link(staging, target)
            staging.unlink()
        published = True
        return ConversionResult(operation, target)
    except UserExamError:
        raise
    except FileExistsError:
        raise UserExamError("同名输出文件刚刚被创建，请重试。") from None
    except OSError:
        raise UserExamError("文件操作失败。请检查源文件读权限、同目录写权限和可用磁盘空间。") from None
    except Exception:
        raise UserExamError("数据库转换或校验失败，未生成可用输出文件。") from None
    finally:
        if not published:
            _cleanup_staging(staging)


KNOWN_TABLES = ("baseInfo", "macAddress")


def inspect_database(path):
    """Read the already-plaintext database and return its known tables.

    Only the two tables this tool understands are read. Nothing is written, no
    cipher is involved, and unknown tables are reported by name only so an
    operator can see whether the file is the expected shape.
    """
    source = Path(path).resolve(strict=True)
    if not source.is_file():
        raise UserExamError("所选路径不是数据库文件。")
    try:
        with source.open("rb") as database:
            header = database.read(len(SQLITE_HEADER))
    except OSError as exc:
        raise UserExamError("无法读取所选数据库文件。") from exc
    if header != SQLITE_HEADER:
        raise UserExamError("该文件仍是加密数据库；请先解密再查看内容。")

    connection = _sqlite_readonly(source)
    try:
        if connection.execute("PRAGMA integrity_check").fetchone() != ("ok",):
            raise UserExamError("数据库完整性检查未通过。")
        present = [
            row[0]
            for row in connection.execute(
                "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
            )
        ]
        tables = {}
        for name in KNOWN_TABLES:
            if name not in present:
                continue
            cursor = connection.execute(f"SELECT * FROM {_quoted_identifier(name)}")
            columns = [description[0] for description in cursor.description]
            rows = [dict(zip(columns, row)) for row in cursor.fetchall()]
            tables[name] = {"columns": columns, "rows": rows}
        return {"path": str(source), "tables": tables, "other_tables": [n for n in present if n not in KNOWN_TABLES]}
    except UserExamError:
        raise
    except sqlite3.DatabaseError:
        raise UserExamError("所选文件不是有效且可读的 SQLite 数据库。") from None
    finally:
        connection.close()


SELFTEST_ROWS = ("self-test-token", "196586", "自检大学", 0)


def run_selftest(keep_dir=None):
    """End-to-end self test: plain -> encrypted -> plain, inside a temp directory.

    This is the only check that proves the *packaged* runtime works: it must
    load the SQLite3MC extension, key the database, rekey it back and read the
    rows through the plain sqlite3 module afterwards. Used by build-exe.ps1 to
    verify the produced exe instead of trusting that PyInstaller exited 0.
    """
    backend = _load_backend()
    information = {
        "tool_version": TOOL_VERSION,
        "cipher": "aes256cbc",
        "kdf_iter": 4001,
        "apsw_module": str(getattr(backend, "__file__", "?")),
        "sqlite3mc": str(getattr(backend, "mc_version", "")),
        "sqlite": str(backend.sqlitelibversion()),
        "frozen": bool(getattr(sys, "frozen", False)),
        "meipass": str(getattr(sys, "_MEIPASS", "")),
    }

    workdir = Path(keep_dir) if keep_dir else Path(tempfile.mkdtemp(prefix="userexam-selftest-"))
    workdir.mkdir(parents=True, exist_ok=True)
    plain = workdir / "UserExamInfo.decrypted.db"
    try:
        connection = sqlite3.connect(str(plain))
        try:
            connection.execute(
                "CREATE TABLE baseInfo(Token TEXT, schoolId TEXT, schoolName TEXT, thirdFild INTEGER)"
            )
            connection.execute("CREATE TABLE macAddress(Address TEXT, Flag TEXT)")
            connection.execute("INSERT INTO baseInfo VALUES(?,?,?,?)", SELFTEST_ROWS)
            connection.execute("INSERT INTO macAddress VALUES('','wsh')")
            connection.commit()
        finally:
            connection.close()

        encrypted = transform_database(plain)
        if encrypted.operation != "encrypt":
            raise UserExamError(f"自检失败：明文库应被判为加密方向，实际 {encrypted.operation}")
        if infer_operation(encrypted.output_path) != "decrypt":
            raise UserExamError("自检失败：加密后的文件头仍是明文 SQLite 头。")

        decrypted = transform_database(encrypted.output_path)
        if decrypted.operation != "decrypt":
            raise UserExamError(f"自检失败：加密库应被判为解密方向，实际 {decrypted.operation}")

        report = inspect_database(decrypted.output_path)
        rows = report["tables"]["baseInfo"]["rows"]
        if len(rows) != 1 or str(rows[0]["schoolId"]) != SELFTEST_ROWS[1]:
            raise UserExamError(f"自检失败：往返后数据不一致，实际 {rows}")
        if report["other_tables"]:
            raise UserExamError(f"自检失败：往返后出现多余表 {report['other_tables']}")

        # The plaintext copy produced by the exe must also be readable by the
        # stock sqlite3 module: no SQLite3MC dependency in the result.
        connection = sqlite3.connect(str(decrypted.output_path))
        try:
            row = connection.execute("SELECT schoolId FROM baseInfo").fetchone()
        finally:
            connection.close()
        if not row or str(row[0]) != SELFTEST_ROWS[1]:
            raise UserExamError("自检失败：标准库 sqlite3 无法读取往返后的明文库。")

        information["roundtrip"] = "encrypt->decrypt OK"
        information["row"] = row[0]
        return information
    finally:
        if not keep_dir:
            shutil.rmtree(workdir, ignore_errors=True)


def _hide_own_console():
    """Hide the console window that the console bootloader always allocates.

    The single-file build uses the console bootloader so that ``--inspect`` and
    other CLI flags keep a working stdout. When no arguments are given we run
    the GUI, and the console window would just sit behind the dialog.
    """
    if os.name != "nt":
        return
    try:
        import ctypes

        handle = ctypes.windll.kernel32.GetConsoleWindow()
        if handle:
            ctypes.windll.user32.ShowWindow(handle, 0)
    except Exception:
        pass


def run_gui():
    import tkinter as tk
    from tkinter import filedialog, messagebox, ttk

    root = tk.Tk()
    root.withdraw()
    source_name = filedialog.askopenfilename(
        parent=root,
        title="选择要加密或解密的 SQLite 数据库",
        filetypes=(("SQLite 数据库", "*.db *.sqlite *.sqlite3"), ("所有文件", "*.*")),
    )
    if not source_name:
        root.destroy()
        return

    progress = tk.Toplevel(root)
    progress.title("数据库转换")
    progress.resizable(False, False)
    progress.protocol("WM_DELETE_WINDOW", lambda: None)
    ttk.Label(progress, text="正在创建并校验副本，请稍候…", padding=(22, 18, 22, 8)).pack()
    bar = ttk.Progressbar(progress, mode="indeterminate", length=280)
    bar.pack(padx=22, pady=(0, 18))
    bar.start(12)
    progress.update_idletasks()

    results = queue.Queue(maxsize=1)

    def worker():
        try:
            results.put((transform_database(source_name), None))
        except UserExamError as exc:
            results.put((None, str(exc)))
        except Exception:
            results.put((None, "发生意外错误；未确认的临时文件不会作为结果发布。"))

    def finish_when_ready():
        try:
            result, error = results.get_nowait()
        except queue.Empty:
            root.after(100, finish_when_ready)
            return

        bar.stop()
        progress.destroy()
        if error:
            messagebox.showerror("数据库转换失败", error, parent=root)
        else:
            action = "加密" if result.operation == "encrypt" else "解密"
            messagebox.showinfo(
                "数据库转换完成",
                f"{action}副本已生成并通过完整性校验。\n\n{result.output_path}\n\n源文件未被修改。",
                parent=root,
            )
        root.destroy()

    threading.Thread(target=worker, name="userexam-db-conversion", daemon=True).start()
    root.after(100, finish_when_ready)
    root.mainloop()


def _force_utf8_console():
    """Windows consoles default to GBK and mangle Chinese output."""
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if reconfigure is not None:
            try:
                reconfigure(encoding="utf-8", errors="replace")
            except (ValueError, OSError):
                pass


def _run_cli(argv):
    import argparse
    import json

    _force_utf8_console()

    parser = argparse.ArgumentParser(
        prog="userexam_crypto",
        description="加密或解密 UserExamInfo SQLite 数据库副本；也可只读查看已解密的库。",
    )
    parser.add_argument("--input", "-i", help="要处理的数据库文件路径")
    parser.add_argument("--output-dir", "-o", help="输出目录（缺省与源文件同目录）")
    parser.add_argument(
        "--inspect",
        action="store_true",
        help="只读查看已解密的库，输出 baseInfo / macAddress 的 JSON",
    )
    parser.add_argument("--json", action="store_true", help="以 JSON 输出结果")
    parser.add_argument("--version", action="version", version=f"userexam-crypto {TOOL_VERSION}")
    parser.add_argument(
        "--selftest",
        action="store_true",
        help="自检：在临时目录做一次 明文→加密→解密 往返，验证随包 SQLite3MC 可加载",
    )
    args = parser.parse_args(argv)

    if args.selftest:
        try:
            report = run_selftest()
        except UserExamError as exc:
            print(f"自检失败：{exc}", file=sys.stderr)
            return 1
        print(json.dumps(report, ensure_ascii=False, indent=2, default=str))
        return 0

    if not args.input:
        parser.error("需要 --input 指定数据库文件")

    try:
        if args.inspect:
            report = inspect_database(args.input)
            print(json.dumps(report, ensure_ascii=False, indent=2, default=str))
            return 0

        result = transform_database(args.input)
        payload = {"operation": result.operation, "output_path": str(result.output_path)}
        if args.output_dir:
            destination = Path(args.output_dir)
            destination.mkdir(parents=True, exist_ok=True)
            moved = destination / result.output_path.name
            shutil.move(str(result.output_path), str(moved))
            payload["output_path"] = str(moved)
        if args.json:
            print(json.dumps(payload, ensure_ascii=False))
        else:
            action = "加密" if result.operation == "encrypt" else "解密"
            print(f"{action}副本已生成并通过完整性校验：{payload['output_path']}")
            print("源文件未被修改。")
        return 0
    except UserExamError as exc:
        if args.json:
            print(json.dumps({"error": str(exc)}, ensure_ascii=False))
        else:
            print(f"转换失败：{exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    if len(sys.argv) > 1:
        raise SystemExit(_run_cli(sys.argv[1:]))
    _hide_own_console()
    run_gui()
