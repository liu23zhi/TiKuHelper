<#
.SYNOPSIS
    Build the single-file userexam-crypto.exe (administrator offline tool).

.DESCRIPTION
    Produces tools/userexam-crypto/dist/userexam-crypto.exe (~15 MB).
    The artifact is git-ignored: it is a build product, not source.

    The cipher key is NOT bundled into the exe. Deploy the exe together with a
    password.txt (git-ignored, one line) or set USEREXAM_PASSWORD on the target
    machine. This repository is public; the key must never be committed.

.NOTES
    Requires 64-bit CPython 3.11 and PyInstaller 6.x.
#>
[CmdletBinding()]
param(
    # Install the SQLite3MC runtime into .\_vendor before building.
    [switch]$InstallVendor,
    # Skip the post-build smoke test.
    [switch]$SkipSmokeTest
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$python = 'py'
$pythonArgs = @('-3.11')

Write-Host '[1/5] Checking Python 3.11 x64 ...'
$version = & $python @pythonArgs -c "import sys, struct; print(f'{sys.version_info.major}.{sys.version_info.minor} {struct.calcsize(""P"") * 8} {sys.implementation.name}')"
if ($LASTEXITCODE -ne 0) {
    throw "py -3.11 is unavailable. Install 64-bit CPython 3.11 (with Tkinter)."
}
$parts = $version.Trim().Split(' ')
if ($parts[0] -ne '3.11' -or $parts[1] -ne '64') {
    throw "Need CPython 3.11 x64, found '$($version.Trim())'."
}
Write-Host "      Python $($version.Trim()) OK"

Write-Host '[2/5] Checking PyInstaller ...'
& $python @pythonArgs -m PyInstaller --version *> $null
if ($LASTEXITCODE -ne 0) {
    Write-Host '      installing pyinstaller ...'
    & $python @pythonArgs -m pip install --quiet pyinstaller
    if ($LASTEXITCODE -ne 0) { throw 'Failed to install PyInstaller.' }
}
$pyiVersion = (& $python @pythonArgs -m PyInstaller --version).Trim()
Write-Host "      PyInstaller $pyiVersion OK"

$vendorPyd = Join-Path $PSScriptRoot '_vendor\apsw\__init__.cp311-win_amd64.pyd'
if ($InstallVendor -or -not (Test-Path -LiteralPath $vendorPyd)) {
    Write-Host '[3/5] Installing apsw-sqlite3mc into _vendor ...'
    $vendorRoot = Join-Path $PSScriptRoot '_vendor'
    New-Item -ItemType Directory -Force -Path $vendorRoot | Out-Null
    & $python @pythonArgs -m pip install --quiet --upgrade --target $vendorRoot apsw-sqlite3mc==3.54.0.0
    if ($LASTEXITCODE -ne 0) { throw 'Failed to install apsw-sqlite3mc.' }
}
if (-not (Test-Path -LiteralPath $vendorPyd)) {
    throw "SQLite3MC runtime still missing: $vendorPyd"
}
$vendorSize = (Get-Item -LiteralPath $vendorPyd).Length
Write-Host "[3/5] _vendor runtime present ($([math]::Round($vendorSize / 1MB, 2)) MB) OK"

Write-Host '[4/5] Building single-file exe ...'
& $python @pythonArgs -m PyInstaller --noconfirm --clean userexam_crypto.spec
if ($LASTEXITCODE -ne 0) { throw 'PyInstaller build failed.' }
$exePath = Join-Path $PSScriptRoot 'dist\userexam-crypto.exe'
if (-not (Test-Path -LiteralPath $exePath)) { throw "Build produced no exe at $exePath" }

$exeHash = (Get-FileHash -LiteralPath $exePath -Algorithm SHA256).Hash.ToLowerInvariant()
$exeSize = (Get-Item -LiteralPath $exePath).Length
Write-Host "[4/5] Built $exePath"
Write-Host "      size=$exeSize sha256=$exeHash"

if (-not $SkipSmokeTest) {
    Write-Host '[5/5] Smoke test: --inspect against a plaintext fixture ...'
    $fixture = Join-Path $PSScriptRoot 'testdata\UserExamInfo.decrypted.db'
    if (-not (Test-Path -LiteralPath $fixture)) {
        $fixture = Join-Path $PSScriptRoot 'testdata\UserExamInfo.db'
    }
    if (Test-Path -LiteralPath $fixture) {
        $output = & $exePath --inspect -i $fixture 2>&1
        Write-Host ($output | Out-String)
        if ($LASTEXITCODE -ne 0) { throw 'Smoke test failed: --inspect returned non-zero.' }
        Write-Host '[5/5] Smoke test OK'
    } else {
        Write-Host '      testdata fixture not found; skipped. Create testdata\UserExamInfo.decrypted.db to enable.'
    }
} else {
    Write-Host '[5/5] Smoke test skipped (-SkipSmokeTest).'
}

Write-Host ''
Write-Host 'Done. The exe does NOT contain the cipher key:'
Write-Host '  * put password.txt (one line) next to the exe, or'
Write-Host '  * set USEREXAM_PASSWORD in the environment.'
Write-Host 'dist/ is git-ignored; do not commit build products.'
