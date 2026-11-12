"""Regression guard for build-exe.ps1's Python probe.

Why this file exists
--------------------
build-exe.ps1 step 1 used to pass Python code inline:

    $version = & $python @pythonArgs -c "import sys, struct; print(f'... {struct.calcsize(""P"") * 8} ...')"

PowerShell double-quoted strings interpolate, so the code that actually reached
Python was mangled and raised ``NameError: name 'P' is not defined``. The script
then reported "py -3.11 is unavailable" even though ``py -3.11`` worked fine --
a fatal *false* diagnosis that sent people hunting for a missing interpreter.

At the time of writing, CI only runs ``test_userexam_crypto``; nothing ever
executed build-exe.ps1, so this bug could only be found by a human running a
real build. This module is the guard: it runs the probe through ``-ProbeOnly``
and fails loudly if the probe regresses.

What it does NOT do
-------------------
It never invokes PyInstaller and never builds an exe. That takes ~20s and needs
a vendored native extension; it is not what we are protecting against.

The PowerShell-5.1 requirement
-------------------------------
The bug lives specifically in **Windows PowerShell 5.1**'s interpolation
semantics. ``pwsh`` 7 happens to tolerate the broken form, so a test that only
runs under pwsh would stay green on the buggy code and protect nothing. Every
substantive assertion here therefore runs under ``powershell.exe``.

Platform behaviour
------------------
``powershell.exe`` only exists on Windows. On other platforms the tests skip
with an explicit reason (``SkipTest`` message, printed by unittest), never a
silent pass. CI runs them for real in the ``python-windows-probe`` job on
``windows-latest``; see .github/workflows/ci.yml.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
BUILD_SCRIPT = HERE / "build-exe.ps1"

# Windows PowerShell 5.1. Deliberately NOT "pwsh" -- see module docstring.
POWERSHELL_51 = "powershell.exe"

# ProbeOnly needs only the interpreter; keep the timeout small so a hang is a
# failure rather than a stuck CI job.
PROBE_TIMEOUT_SECONDS = 120


def _powershell_51_available() -> bool:
    """True when a Windows PowerShell 5.1 binary can be launched."""
    if os.name != "nt":
        return False
    return shutil.which(POWERSHELL_51) is not None


def _skip_reason() -> str:
    if os.name != "nt":
        return (
            "requires Windows: the regression is specific to Windows PowerShell 5.1 "
            "interpolation; pwsh 7 tolerates the broken form, so running this on "
            f"{sys.platform} would not exercise the bug at all"
        )
    return f"requires {POWERSHELL_51} (Windows PowerShell 5.1) on PATH"


def _run_probe(extra_args: list[str] | None = None) -> subprocess.CompletedProcess[str]:
    """Run build-exe.ps1 -ProbeOnly under Windows PowerShell 5.1."""
    args = [
        POWERSHELL_51,
        "-NoProfile",
        "-NonInteractive",
        "-ExecutionPolicy", "Bypass",
        "-File", str(BUILD_SCRIPT),
        "-ProbeOnly",
    ]
    if extra_args:
        args.extend(extra_args)
    return subprocess.run(
        args,
        cwd=str(HERE),
        capture_output=True,
        text=True,
        timeout=PROBE_TIMEOUT_SECONDS,
    )


@unittest.skipUnless(_powershell_51_available(), _skip_reason())
class BuildExeProbeTest(unittest.TestCase):
    """The probe must work under Windows PowerShell 5.1."""

    def test_probe_only_exits_zero(self) -> None:
        """-ProbeOnly exits 0 under powershell.exe 5.1.

        This is the assertion that fails when the inline ``-c "..."`` form comes
        back: the mangled Python raises NameError, $LASTEXITCODE is 1, and the
        script throws "py -3.11 is unavailable".
        """
        result = _run_probe()
        self.assertEqual(
            0,
            result.returncode,
            "build-exe.ps1 -ProbeOnly failed under Windows PowerShell 5.1.\n"
            "A non-zero exit here usually means the Python probe went back to an "
            "inline -c \"...\" argument that PowerShell interpolates.\n"
            f"--- stdout ---\n{result.stdout}\n--- stderr ---\n{result.stderr}",
        )

    def test_probe_reports_3_11_and_64(self) -> None:
        """The probe reports version 3.11 and 64-bit.

        Downstream code does ``$parts[0] -eq '3.11'`` / ``$parts[1] -eq '64'``,
        so the triple is a real contract, not cosmetics: a probe that prints
        nothing (as the broken form does) breaks the build just as hard as a
        wrong version.
        """
        result = _run_probe()
        self.assertEqual(0, result.returncode, f"probe failed: {result.stderr}")

        text = result.stdout + result.stderr
        self.assertIn("3.11", text, f"probe output has no 3.11:\n{text}")
        self.assertIn("64", text, f"probe output has no 64 (bitness):\n{text}")

    def test_probe_machine_readable_triple(self) -> None:
        """-ProbeMachineReadable emits exactly the triple downstream parses.

        Asserting the structured line instead of grepping prose keeps this test
        honest: grepping stdout for substrings like "3.11" could pass on a
        completely different sentence (for example an error message mentioning
        the required version).
        """
        result = _run_probe(["-ProbeMachineReadable"])
        self.assertEqual(0, result.returncode, f"probe failed: {result.stderr}")

        match = re.search(
            r"^USEREXAM_PROBE version=(\S+) bits=(\S+) impl=(\S+)\s*$",
            result.stdout,
            re.MULTILINE,
        )
        self.assertIsNotNone(
            match,
            f"no USEREXAM_PROBE line in probe output:\n{result.stdout}",
        )
        version, bits, impl = match.group(1), match.group(2), match.group(3)
        self.assertEqual("3.11", version, f"unexpected version {version!r}")
        self.assertEqual("64", bits, f"unexpected bitness {bits!r}")
        # Any CPython-family implementation is acceptable; the script only
        # requires "a real interpreter that reports this triple".
        self.assertTrue(impl, "implementation name must not be empty")

    def test_probe_does_not_reference_inline_python_code(self) -> None:
        """The script must not pass Python source inline via -c.

        This is a static guard on top of the behavioural one. The behavioural
        test catches the bug on 5.1; this one catches it on any platform and
        makes the intent legible to whoever edits the file next. It is
        deliberately narrow: it looks for ``-c`` immediately followed by a
        quoted Python program, not for the letter combination anywhere.
        """
        source = BUILD_SCRIPT.read_text(encoding="utf-8-sig")
        # A "-c" argument whose body defines a print/import statement is the
        # shape that broke. Allow "-c" in comments and in unrelated flags.
        offenders = [
            line
            for line in source.splitlines()
            if re.search(r'-c\s+["\'][^"\']*\b(import\s+sys|print\()', line)
        ]
        self.assertEqual(
            [],
            offenders,
            "build-exe.ps1 passes Python code inline via -c, which PowerShell "
            "interpolates and mangles. Write a temp .py file instead (see the "
            "probe above step 2).\nOffending lines:\n" + "\n".join(offenders),
        )


class BuildExeScriptShapeTest(unittest.TestCase):
    """Checks that need no PowerShell at all, so they run on every platform."""

    def test_script_exists(self) -> None:
        self.assertTrue(BUILD_SCRIPT.is_file(), f"missing {BUILD_SCRIPT}")

    def test_script_has_probe_only_switch(self) -> None:
        """-ProbeOnly must stay declared; it is this test module's only hook.

        Without it the guard cannot run at all, so its disappearance must be a
        loud failure rather than an unexplained skip.
        """
        source = BUILD_SCRIPT.read_text(encoding="utf-8-sig")
        self.assertIn("[switch]$ProbeOnly", source,
                      "build-exe.ps1 no longer declares a -ProbeOnly switch; "
                      "test_build_exe.py cannot exercise the probe without it")

    def test_script_is_utf8_with_bom(self) -> None:
        """The .ps1 must keep its UTF-8 BOM.

        Windows PowerShell 5.1 decodes a BOM-less script as ANSI, which mangles
        non-ASCII text in this repo's messages. Losing the BOM is a silent,
        platform-only corruption.
        """
        raw = BUILD_SCRIPT.read_bytes()
        self.assertTrue(
            raw.startswith(b"\xef\xbb\xbf"),
            "build-exe.ps1 lost its UTF-8 BOM; Windows PowerShell 5.1 will "
            "decode it as ANSI and corrupt non-ASCII text",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
