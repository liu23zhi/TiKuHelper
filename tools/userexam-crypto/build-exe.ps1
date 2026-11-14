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

.PARAMETER ProbeOnly
    Run only step 1 (the Python 3.11 x64 probe) and exit. Used by
    test_build_exe.py as a regression guard.

    The probe must be exercised under Windows PowerShell 5.1, not just pwsh:
    the original implementation passed Python code inline via -c "..." and
    PowerShell double quotes interpolate, so the code reaching Python was
    mangled (NameError: name 'P' is not defined) and the script wrongly
    reported "py -3.11 is unavailable". Windows PowerShell 5.1 is where that
    interpolation bites; pwsh 7 happens to survive it. A test that only runs
    under pwsh would go green on broken code.

.PARAMETER ProbeMachineReadable
    With -ProbeOnly, print the probe result on one line as
        USEREXAM_PROBE version=<ver> bits=<bits> impl=<impl>
    so a test can assert the exact triple instead of grepping human prose.
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
    [string]$Password,
    # Run only the Python probe (step 1) and exit. Regression-guard hook.
    [switch]$ProbeOnly,
    # With -ProbeOnly, emit a single machine-readable result line.
    [switch]$ProbeMachineReadable
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$python = 'py'
$pythonArgs = @('-3.11')
$exePath = Join-Path $PSScriptRoot 'dist\userexam-crypto.exe'
$totalSteps = 5

Write-Host "[1/$totalSteps] Checking Python 3.11 x64 ..."
    $pyProbe = Join-Path ([System.IO.Path]::GetTempPath()) ("cx-py-probe-" + [guid]::NewGuid().ToString("N") + ".py")
    try {
        Set-Content -LiteralPath $pyProbe -Encoding ascii -Value @('import sys, struct', 'v = chr(46).join([str(sys.version_info.major), str(sys.version_info.minor)]); print(v, struct.calcsize("P") * 8, sys.implementation.name)')
        $version = & $python @pythonArgs $pyProbe
    } finally {
        Remove-Item -LiteralPath $pyProbe -Force -ErrorAction SilentlyContinue
    }
if ($LASTEXITCODE -ne 0) {
    throw "py -3.11 is unavailable. Install 64-bit CPython 3.11 (with Tkinter)."
}
$parts = $version.Trim().Split(' ')
if ($parts[0] -ne '3.11' -or $parts[1] -ne '64') {
    throw "Need CPython 3.11 x64, found '$($version.Trim())'."
}
Write-Host "      Python $($version.Trim()) OK"

if ($ProbeOnly) {
    # Regression hook for test_build_exe.py. Exit 0 only when the probe produced
    # a version triple the rest of the script can actually parse; anything else
    # must be a non-zero exit so CI turns red instead of silently continuing.
    if ($ProbeMachineReadable) {
        Write-Output ("USEREXAM_PROBE version={0} bits={1} impl={2}" -f $parts[0], $parts[1], $(if ($parts.Count -ge 3) { $parts[2] } else { '' }))
    }
    Write-Host "[1/$totalSteps] Probe-only mode: Python probe passed, exiting."
    exit 0
}

Write-Host "[2/$totalSteps] Checking PyInstaller ..."
# ⚠️ 不能用 `*> $null` 探测外部命令是否成功：
#
# 在 $ErrorActionPreference = 'Stop' 下，原生命令写到 stderr 的内容会变成**终止错误**，
# 而 `*>` 重定向**挡不住**这个转换。所以当 PyInstaller 未安装时，这一行会直接抛
#   "py.exe : ...: No module named PyInstaller"
# 让脚本在「检查」阶段就崩掉，永远走不到下面的 `if ($LASTEXITCODE -ne 0)` 安装分支。
#
# CI 上实测就是这样失败的（run 43571220472：`[2/5] Checking PyInstaller ...` 之后立即
# 报 No module named PyInstaller，而本地因为装了包所以复现不出来）。
#
# 正确写法：临时放宽 EAP，只吞输出不吞退出码。
$pyiProbeOk = $false
$savedEap = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
try {
    & $python @pythonArgs -m PyInstaller --version *> $null
    $pyiProbeOk = ($LASTEXITCODE -eq 0)
} finally {
    $ErrorActionPreference = $savedEap
}
if (-not $pyiProbeOk) {
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

# 真正的 one-file 构建在 dist\ 里只留 exe。出现别的**构建产物**说明 spec 退化成了文件夹式，
# 「无需安装」的承诺就是假的。
#
# ⚠️ 必须排除 `.gitkeep`：dist\ 被 .gitignore 整体排除，加一个 .gitkeep 是为了让空目录能入库
# （否则 `!/dist/.gitkeep` 那条取反规则永远不会生效 —— git 不会进入被整体排除的目录）。
# 它是**占位文件**，不是构建产物。之前断言写成 `Count -ne 1`，本地有 exe 时恰好通过，
# CI 从干净 checkout 构建时就是 `.gitkeep + exe` 两个 ⇒ 恒失败。
$distEntries = @(Get-ChildItem -LiteralPath (Split-Path -Parent $exePath) -Force | Where-Object { $_.Name -ne '.gitkeep' })
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
            # 同 L113 的理由：$ErrorActionPreference='Stop' 下原生命令写 stderr 会变成终止错误，
            # `*>` 挡不住这个转换。用同样的放宽-恢复写法，只吞输出不吞退出码。
            $selftestOk = $false
            $savedEapB = $ErrorActionPreference
            $ErrorActionPreference = 'Continue'
            try {
                & (Join-Path $isolated 'userexam-crypto.exe') --selftest *> $null
                $selftestOk = ($LASTEXITCODE -eq 0)
            } finally {
                $ErrorActionPreference = $savedEapB
            }
            if (-not $selftestOk) { throw 'Runtime check B failed: exe alone could not run.' }
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
