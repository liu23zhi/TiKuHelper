# -*- mode: python ; coding: utf-8 -*-
"""PyInstaller spec for the standalone UserExamInfo database tool.

The SQLite3MC runtime is a native ``.pyd`` that must sit at
``<_MEIPASS>/_vendor/apsw/__init__.cp311-win_amd64.pyd`` so that
``userexam_crypto._resource_root()`` can put ``_vendor`` on ``sys.path``.
``datas`` (not ``binaries``) is used on purpose: PyInstaller keeps the
directory layout for data files, while ``binaries`` may flatten it.

The cipher key is deliberately NOT bundled. ``password.txt`` is placed next to
the exe at deployment time, or ``USEREXAM_PASSWORD`` is set in the environment.
"""

from pathlib import Path

from PyInstaller.utils.hooks import collect_submodules

PROJECT = Path(SPECPATH)

VENDOR_PYD = PROJECT / "_vendor" / "apsw" / "__init__.cp311-win_amd64.pyd"
if not VENDOR_PYD.is_file():
    raise SystemExit(
        f"SQLite3MC runtime missing: {VENDOR_PYD}\n"
        "Run: py -3.11 -m pip install --target .\\_vendor apsw-sqlite3mc==3.54.0.0"
    )

datas = [
    (str(VENDOR_PYD), "_vendor/apsw"),
]

# NOTE: "apsw" is deliberately NOT in hiddenimports. It is a single-file native
# extension (no apsw/__init__.py anywhere), so PyInstaller's import scanner can
# only report "Hidden import 'apsw' not found" and would not add anything worth
# adding. The runtime reaches it through sys.path — see _load_backend(), which
# inserts <_MEIPASS>/_vendor before importing apsw.
a = Analysis(
    [str(PROJECT / "userexam_crypto.py")],
    pathex=[str(PROJECT)],
    binaries=[],
    datas=datas,
    hiddenimports=collect_submodules("tkinter"),
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=["numpy", "pandas", "matplotlib", "PIL", "setuptools", "pip"],
    noarchive=False,
)

pyz = PYZ(a.pure)

# console=True on purpose: --inspect and other CLI flags need a working stdout.
# The script hides the console window itself when it falls back to the GUI
# (see _hide_own_console), so a windowed bootloader is not required.
exe = EXE(
    pyz,
    a.scripts,
    a.binaries,
    a.datas,
    [],
    name="userexam-crypto",
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=False,
    upx_exclude=[],
    runtime_tmpdir=None,
    console=True,
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
)
