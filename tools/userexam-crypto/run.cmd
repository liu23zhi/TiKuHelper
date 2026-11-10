@echo off
setlocal
cd /d "%~dp0"

REM The cipher key is never stored in this repository. Provide it at run time:
REM   set USEREXAM_PASSWORD=<key>
REM or drop a password.txt (one line, git-ignored) next to this script.
if "%USEREXAM_PASSWORD%"=="" if not exist "%~dp0password.txt" (
    echo The cipher key is not configured.
    echo Set USEREXAM_PASSWORD, or create password.txt next to this script.
    echo See README.md for details.
    pause
    exit /b 1
)

py -3.11 "%~dp0userexam_crypto.py"
if errorlevel 1 (
    echo.
    echo Unable to start. Install 64-bit CPython 3.11 with Tkinter and the Python launcher.
    echo See README.md for details.
    pause
)
