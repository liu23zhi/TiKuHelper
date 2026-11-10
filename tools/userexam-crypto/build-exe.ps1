<#
.SYNOPSIS
    Build the single-file userexam-crypto.exe (administrator offline tool).

.DESCRIPTION
    One command produces tools/userexam-crypto/dist/userexam-crypto.exe: a real
    single file. dist\ contains the exe and nothing else — no _vendor folder, no
    Python installation is needed on the target machine.

    The SQLite3MC runtime is a native extension (apsw __init__.cp311-win_amd64.pyd,
    ~2.8 MB). The spec ships it as a *data* entry under _vendor/apsw so the frozen
    app can put <_MEIPASS>/_vendor on sys.path; PyInstaller then packs it into the
    one-file archive like any other bundled file.

    Verification is not "PyInstaller exited 0". The exe is run twice:
      1. --selftest with the whole PATH stripped to %SystemRoot%\system32,
         proving the process needs no interpreter;
      2. the same binary is copied alone into an empty directory and asked to
         convert a real encrypted sample, if one is available.

    The cipher key is NOT bundled into the exe. Deploy the exe together with a
    password.txt (git-ignored, one line) or set USEREXAM_PASSWORD on the target
    machine. This repository is public; the key must never be committed.

.NOTES
    Requires 64-bit CPython 3.11 and PyInstaller 6.x on the build machine.
#>
[CmdletBinding()]
param(
    # Install the SQLite3MC runtime into .\_vendor before building.
    [switch]$InstallVendor,
    # Skip the post-build runtime checks (build only).
    [switch]$SkipSmokeTest,
    # Optional real encrypted UserExamInfo.db used for the end-to-end check.
    [string]$RealSample,
    # Password for $RealSample; only needed when the sample is converted.
    [string]$Password
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$python = 'py'
$pythonArgs = @('-3.11')
$exePath = Join-Path $PSScriptRoot 'dist\userexam-crypto.exe'
$totalSteps = 5

Write-Host "[1/$totalSteps] Checking Python 3.11 x64 ..."
$version = & $python @pythonArgs -c "import sys, struct; print(f'{sys.version_info.major}.{sys.version_info.minor} {struct.calcsize(""P"") * 8} {sys.implementation.name}')"
if ($LASTEXITCODE -ne 0) {
    throw "py -3.11 is unavailable. Install 64-bit CPython 3.11 (with Tkinter)."
}
$parts = $version.Trim().Split(' ')
if ($parts[0] -ne '3.11' -or $parts[1] -ne '64') {
    throw "Need CPython 3.11 x64, found '$($version.Trim())'."
}
Write-Host "      Python $($version.Trim()) OK"

Write-Host "[2/$totalSteps] Checking PyInstaller ..."
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
    Write-Host "[3/$totalSteps] Installing apsw-sqlite3mc into _vendor ..."
    $vendorRoot = Join-Path $PSScriptRoot '_vendor'
    New-Item -ItemType Directory -Force -Path $vendorRoot | Out-Null
    & $python @pythonArgs -m pip install --quiet --upgrade --target $vendorRoot apsw-sqlite3mc==3.54.0.0
    if ($LASTEXITCODE -ne 0) { throw 'Failed to install apsw-sqlite3mc.' }
}
if (-not (Test-Path -LiteralPath $vendorPyd)) {
    throw "SQLite3MC runtime still missing: $vendorPyd"
}
$vendorSize = (Get-Item -LiteralPath $vendorPyd).Length
Write-Host "[3/$totalSteps] _vendor runtime present ($([math]::Round($vendorSize / 1MB, 2)) MB) OK"

Write-Host "[4/$totalSteps] Building single-file exe ..."
& $python @pythonArgs -m PyInstaller --noconfirm --clean userexam_crypto.spec
if ($LASTEXITCODE -ne 0) { throw 'PyInstaller build failed.' }
if (-not (Test-Path -LiteralPath $exePath)) { throw "Build produced no exe at $exePath" }

# A true one-file build leaves exactly one file in dist\. Anything else means the
# spec silently produced a folder build and the "no install" promise is a lie.
$distEntries = @(Get-ChildItem -LiteralPath (Split-Path -Parent $exePath) -Force)
if ($distEntries.Count -ne 1 -or $distEntries[0].Name -ne 'userexam-crypto.exe') {
    throw "dist\ is not single-file: $($distEntries.Name -join ', ')"
}

$exeHash = (Get-FileHash -LiteralPath $exePath -Algorithm SHA256).Hash.ToLowerInvariant()
$exeSize = (Get-Item -LiteralPath $exePath).Length
Write-Host "[4/$totalSteps] Built $exePath"
Write-Host "      size=$exeSize sha256=$exeHash"

if ($SkipSmokeTest) {
    Write-Host "[5/$totalSteps] Runtime checks skipped (-SkipSmokeTest)."
} else {
    # --- 5a: packaged runtime self test with an interpreter-free PATH ----------
    Write-Host "[5/$totalSteps] Runtime check A: --selftest with Python stripped from PATH ..."
    $savedPath = $env:PATH
    $savedPassword = $env:USEREXAM_PASSWORD
    try {
        if (-not $Password) {
            # Any key works for a self test: it is written into the DB and read
            # back within the same temp directory, never compared to a sample.
            $Password = 'userexam-build-selftest'
        }
        $env:PATH = Join-Path $env:SystemRoot 'system32'
        $env:USEREXAM_PASSWORD = $Password
        $selfTestOutput = & $exePath --selftest 2>&1
        $selfTestCode = $LASTEXITCODE
        $selfTestText = $selfTestOutput | Out-String
        Write-Host $selfTestText
        if ($selfTestCode -ne 0) { throw 'Runtime check A failed: --selftest returned non-zero.' }
        if (-not $selfTestText.Contains('"roundtrip": "encrypt->decrypt OK"')) {
            throw 'Runtime check A failed: self test did not report a round trip.'
        }
        Write-Host '      Runtime check A OK (SQLite3MC loaded, round trip verified)'

        # --- 5b: the same binary alone in an empty directory -------------------
        Write-Host '      Runtime check B: exe alone in an empty directory ...'
        $isolated = Join-Path ([System.IO.Path]::GetTempPath()) ("userexam-isolated-" + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Force -Path $isolated | Out-Null
        try {
            Copy-Item -LiteralPath $exePath -Destination (Join-Path $isolated 'userexam-crypto.exe')
            & (Join-Path $isolated 'userexam-crypto.exe') --selftest *> $null
            if ($LASTEXITCODE -ne 0) { throw 'Runtime check B failed: exe alone could not run.' }
            Write-Host '      Runtime check B OK (single file, no sibling _vendor needed)'

            # --- 5c: real encrypted sample, if one was supplied -----------------
            if ($RealSample) {
                Write-Host "      Runtime check C: real sample $RealSample ..."
                if (-not (Test-Path -LiteralPath $RealSample)) { throw "Real sample not found: $RealSample" }
                $sampleCopy = Join-Path $isolated 'UserExamInfo.db'
                Copy-Item -LiteralPath $RealSample -Destination $sampleCopy
                $decryptOutput = & (Join-Path $isolated 'userexam-crypto.exe') -i $sampleCopy --json 2>&1
                Write-Host ($decryptOutput | Out-String)
                if ($LASTEXITCODE -ne 0) { throw 'Runtime check C failed: decrypting the real sample returned non-zero.' }
                $decryptedPath = Join-Path $isolated 'UserExamInfo.decrypted.db'
                if (-not (Test-Path -LiteralPath $decryptedPath)) { throw 'Runtime check C failed: no decrypted output.' }
                $inspectOutput = & (Join-Path $isolated 'userexam-crypto.exe') --inspect -i $decryptedPath 2>&1
                if ($LASTEXITCODE -ne 0) { throw 'Runtime check C failed: --inspect on the decrypted copy failed.' }
                Write-Host ($inspectOutput | Out-String)

                $reencryptOutput = & (Join-Path $isolated 'userexam-crypto.exe') -i $decryptedPath --json 2>&1
                Write-Host ($reencryptOutput | Out-String)
                if ($LASTEXITCODE -ne 0) { throw 'Runtime check C failed: re-encrypting returned non-zero.' }
                Write-Host '      Runtime check C OK (real sample: decrypt -> inspect -> encrypt)'
            }
        } finally {
            Remove-Item -LiteralPath $isolated -Recurse -Force -ErrorAction SilentlyContinue
        }
    } finally {
        $env:PATH = $savedPath
        if ($null -eq $savedPassword) { Remove-Item Env:\USEREXAM_PASSWORD -ErrorAction SilentlyContinue }
        else { $env:USEREXAM_PASSWORD = $savedPassword }
    }
    Write-Host "[5/$totalSteps] All runtime checks passed."
}

Write-Host ''
Write-Host 'Done. The exe does NOT contain the cipher key:'
Write-Host '  * put password.txt (one line) next to the exe, or'
Write-Host '  * set USEREXAM_PASSWORD in the environment.'
Write-Host 'dist/ is git-ignored; do not commit build products.'
