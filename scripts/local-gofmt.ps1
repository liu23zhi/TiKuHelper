<#
.SYNOPSIS
  本地 gofmt 检查：区分「真格式问题」与「Windows CRLF 假阳性」。

.DESCRIPTION
  CI（.github/workflows/ci.yml 的「检查变更 Go 文件的格式」）在 Linux 上用 LF 检出跑
  `gofmt -l`。本机 core.autocrlf=true 时工作树是 CRLF，`gofmt -l` 会把**仅因换行**而
  需重排的文件也报出来，于是本地无法区分：

    - 真格式问题（LF 下 gofmt 仍要求改）→ CI 会红，必须修
    - CRLF 假阳性（LF 下合规）          → CI 会绿，不用改

  本脚本把每个文件**先归一化成 LF 副本**再判定，从而给出可信分类。判定只用 LF 副本，
  所以结论与开发机的 core.autocrlf / core.eol 无关，与 CI 的 Linux 环境一致。

  三类结果：

    REAL        LF 下 gofmt 仍要求改。CI 会红。用 -Fix 修。
    CRLF-ONLY   只因 CRLF 而报，LF 下合规。CI 会绿。无需改动。
    CLEAN       本来就合规。

  另外会打印 CRLF-NOTE：工作树是 CRLF、但 LF 下也不合规的文件。这类文件用 -Fix 修完
  之后，写回的仍是 LF（本脚本永远输出 LF），会顺带把换行也归一。

.PARAMETER Fix
  就地修复 REAL 文件。写回时**强制 LF**（不还原成 CRLF）—— 与 .gitattributes 的
  `* text=auto eol=lf` 一致，避免「修完格式又被换行改变字节」的反复。

.PARAMETER Path
  显式指定要检查的文件（可多个）。省略时自动取：
    git diff --name-only origin/main HEAD  +  未提交改动（含未跟踪）的 .go 文件。

.PARAMETER Base
  比较基准，默认 origin/main。

.PARAMETER Quiet
  只输出分类统计，不打印每个文件的详情与 gofmt diff。

.EXAMPLE
  # 检查当前分支相对 origin/main 的改动
  .\scripts\local-gofmt.ps1

.EXAMPLE
  # 逐个看清哪些是真问题，并把它们修成 LF
  .\scripts\local-gofmt.ps1 -Fix

.EXAMPLE
  # 只查某几个文件
  .\scripts\local-gofmt.ps1 -Path internal/controller/foo.go, pkg/util/bar.go

.NOTES
  退出码：0 = 没有 REAL；1 = 存在 REAL（与 CI 语义对齐，可直接用于 pre-push 钩子）；2 = 用法/环境错误。
#>

[CmdletBinding()]
param(
    [switch]$Fix,
    [string[]]$Path,
    [string]$Base = 'origin/main',
    [switch]$Quiet,
    # Internal: 供测试注入固定文件列表与工作目录，避免依赖 git 状态。
    [string]$RepoRoot,
    [switch]$Json
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# 基础工具
# ---------------------------------------------------------------------------

$Utf8NoBom = New-Object System.Text.UTF8Encoding($false)

function Get-RepoRoot {
    if ($RepoRoot) { return (Resolve-Path $RepoRoot).Path }
    # 独立调用时可能不在仓库根；用 git 反查，失败则退回当前目录。
    $top = & git rev-parse --show-toplevel 2>$null
    if ($LASTEXITCODE -eq 0 -and $top) { return (Resolve-Path $top).Path }
    return (Get-Location).Path
}

# Get-FileBytes 读原始字节。必须按字节读，不能用 Get-Content：
# Get-Content 会自动拆行、丢换行信息，正是本脚本要判定的东西。
function Get-FileBytes([string]$FullPath) {
    return [System.IO.File]::ReadAllBytes($FullPath)
}

# Test-HasCr 判断字节序列里有没有 CR(13)。有 CR 就说明是 CRLF（或含孤立 CR），
# 需要走 LF 归一化副本判定。
function Test-HasCr([byte[]]$Bytes) {
    foreach ($b in $Bytes) { if ($b -eq 13) { return $true } }
    return $false
}

# ConvertTo-LfBytes 把 CRLF 归一成 LF，**保留原有编码字节**（不动 BOM、不动内容）。
# 只把 \r\n 变 \n；孤立 \r（老 Mac 换行）也一并转成 \n，与 git 的 text=auto 行为一致。
function ConvertTo-LfBytes([byte[]]$Bytes) {
    $out = New-Object System.Collections.Generic.List[byte] ($Bytes.Length)
    for ($i = 0; $i -lt $Bytes.Length; $i++) {
        $b = $Bytes[$i]
        if ($b -eq 13) {
            # \r\n → 只写 \n；孤立 \r → 也写成 \n
            if ($i + 1 -lt $Bytes.Length -and $Bytes[$i + 1] -eq 10) { continue }
            $out.Add([byte]10)
            continue
        }
        $out.Add($b)
    }
    return $out.ToArray()
}

# ---------------------------------------------------------------------------
# 核心：单文件分类
# ---------------------------------------------------------------------------

# Get-GoFmtState 对**给定字节内容**跑 gofmt，返回 @{ NeedFormat; Diff }。
#
# 关键设计：永远把待判定的内容写成临时文件再跑 gofmt，绝不直接把工作树文件交给 gofmt。
# 原因是工作树可能是 CRLF，那会让 gofmt 报「需格式化」——恰好是我们要排除的假阳性。
# 所以调用方必须先把内容归一成 LF 再传进来。
function Get-GoFmtState([byte[]]$LfBytes) {
    $tmp = [System.IO.Path]::Combine([System.IO.Path]::GetTempPath(),
        ('gofmtcheck-' + [System.Guid]::NewGuid().ToString('N') + '.go'))
    try {
        [System.IO.File]::WriteAllBytes($tmp, $LfBytes)

        # gofmt -l 输出非空 = 需要格式化。
        $listed = & gofmt -l $tmp 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw "gofmt -l 执行失败（可能是语法错误）：$listed"
        }
        $needsFormat = -not [string]::IsNullOrWhiteSpace(($listed -join ''))

        $diff = ''
        if ($needsFormat) {
            $raw = & gofmt -d $tmp 2>&1
            # ⚠️ gofmt -d 在「有差异」时**退出码是 1**（不是 0），这是 gofmt 的既定行为，
            # 与「执行失败」共用同一个 exit 1，无法靠退出码区分。所以这里不能因
            # exit 1 就抛错 —— 否则每个真格式问题都会让脚本崩掉（实测踩到）。
            # 只有输出为空且 exit≠0 才算真失败。
            $text = ($raw -join "`n")
            if ($LASTEXITCODE -ne 0 -and [string]::IsNullOrWhiteSpace($text)) {
                throw "gofmt -d 执行失败：$text"
            }
            # -d 的头部含随机临时文件名，替换成稳定占位符，便于测试与人工阅读。
            $diff = $text -replace [regex]::Escape($tmp), '<file>'
        }
        return [pscustomobject]@{ NeedFormat = $needsFormat; Diff = $diff }
    }
    finally {
        if (Test-Path $tmp) { Remove-Item $tmp -Force -ErrorAction SilentlyContinue }
    }
}

# Test-PowerShellFile 判定单个文件，返回分类对象。
#
# 分类决策表（这是全脚本的语义核心）：
#   工作树有 CR + LF 下需格式化  → REAL        （既有真问题，也有换行）
#   工作树有 CR + LF 下合规      → CRLF-ONLY   （纯假阳性）
#   工作树无 CR + LF 下需格式化  → REAL
#   工作树无 CR + LF 下合规      → CLEAN
#
# 注意 REAL 与 CRLF-ONLY 的区别**只在于 LF 副本是否合规**，与工作树有没有 CR 无关 ——
# 工作树有 CR 只用来解释「为什么会误报」，不能用作分类依据。
function Test-GoFile([string]$Root, [string]$RelativePath) {
    $full = [System.IO.Path]::Combine($Root, $RelativePath)
    if (-not (Test-Path $full)) {
        return [pscustomobject]@{
            Path = $RelativePath; Category = 'MISSING'; WorktreeCrlf = $false
            CrCount = 0; Diff = ''; Note = '文件不存在（已删除？）'
        }
    }

    $bytes = Get-FileBytes $full
    $hasCr = Test-HasCr $bytes
    $crCount = 0
    if ($hasCr) { foreach ($b in $bytes) { if ($b -eq 13) { $crCount++ } } }

    # 归一化副本是判定基准。无 CR 时副本就是原字节，省一次拷贝。
    $lfBytes = if ($hasCr) { ConvertTo-LfBytes $bytes } else { $bytes }

    $state = Get-GoFmtState $lfBytes

    if ($state.NeedFormat) {
        $note = if ($hasCr) {
            'LF 下仍需格式化 = 真问题（该文件同时是 CRLF，-Fix 会把换行一并归一）'
        } else {
            'LF 下仍需格式化 = 真问题'
        }
        return [pscustomobject]@{
            Path = $RelativePath; Category = 'REAL'; WorktreeCrlf = $hasCr
            CrCount = $crCount; Diff = $state.Diff; Note = $note
        }
    }

    if ($hasCr) {
        return [pscustomobject]@{
            Path = $RelativePath; Category = 'CRLF-ONLY'; WorktreeCrlf = $true
            CrCount = $crCount; Diff = ''
            Note = '仅换行导致 gofmt 报错，LF 下合规；CI 会绿，无需改动'
        }
    }

    return [pscustomobject]@{
        Path = $RelativePath; Category = 'CLEAN'; WorktreeCrlf = $false
        CrCount = 0; Diff = ''; Note = '合规'
    }
}

# ---------------------------------------------------------------------------
# 范围收集
# ---------------------------------------------------------------------------

function Get-DefaultScope([string]$Root, [string]$BaseRef) {
    $files = New-Object System.Collections.Generic.List[string]

    # 1) 分支相对基准的改动（CI 的口径）
    $committed = & git -C $Root diff --diff-filter=ACMRT --name-only "$BaseRef" HEAD -- '*.go' 2>$null
    if ($LASTEXITCODE -eq 0 -and $committed) {
        foreach ($f in $committed) { if ($f) { $files.Add($f.Trim()) } }
    }

    # 2) 未提交改动（已跟踪的 + 未跟踪的）。CI 看不到这些，但本地必须看 ——
    #    这正是「提交前先自查」的意义。
    $unstaged = & git -C $Root diff --diff-filter=ACMRT --name-only -- '*.go' 2>$null
    if ($LASTEXITCODE -eq 0 -and $unstaged) {
        foreach ($f in $unstaged) { if ($f) { $files.Add($f.Trim()) } }
    }
    $staged = & git -C $Root diff --diff-filter=ACMRT --name-only --cached -- '*.go' 2>$null
    if ($LASTEXITCODE -eq 0 -and $staged) {
        foreach ($f in $staged) { if ($f) { $files.Add($f.Trim()) } }
    }
    $untracked = & git -C $Root ls-files --others --exclude-standard -- '*.go' 2>$null
    if ($LASTEXITCODE -eq 0 -and $untracked) {
        foreach ($f in $untracked) { if ($f) { $files.Add($f.Trim()) } }
    }

    # 去重 + 过滤掉不存在的（被删文件）
    $seen = New-Object 'System.Collections.Generic.HashSet[string]'
    $result = New-Object System.Collections.Generic.List[string]
    foreach ($f in $files) {
        $normalized = $f -replace '\\', '/'
        if (-not $seen.Add($normalized)) { continue }
        if (-not (Test-Path ([System.IO.Path]::Combine($Root, $normalized)))) { continue }
        $result.Add($normalized)
    }
    return $result.ToArray()
}

# ---------------------------------------------------------------------------
# 修复
# ---------------------------------------------------------------------------

# Invoke-GoFmtFix 用 gofmt 改写文件，并**强制输出 LF**。
#
# 为什么不用 `gofmt -w` 直接写工作树文件：gofmt -w 会保留输入文件的换行风格，
# 于是 CRLF 文件修完还是 CRLF，下一次 `gofmt -l` 依然报错，进入死循环。
# 所以这里「gofmt 到 LF 临时副本 → 读回字节 → 原样写回」，写回的必然是 LF。
function Invoke-GoFmtFix([string]$Root, [string]$RelativePath) {
    $full = [System.IO.Path]::Combine($Root, $RelativePath)
    $bytes = Get-FileBytes $full
    $lfBytes = if (Test-HasCr $bytes) { ConvertTo-LfBytes $bytes } else { $bytes }

    $tmp = [System.IO.Path]::Combine([System.IO.Path]::GetTempPath(),
        ('gofmtfix-' + [System.Guid]::NewGuid().ToString('N') + '.go'))
    try {
        [System.IO.File]::WriteAllBytes($tmp, $lfBytes)
        $null = & gofmt -w $tmp 2>&1
        if ($LASTEXITCODE -ne 0) { throw "gofmt -w 失败：$RelativePath" }
        $fixed = [System.IO.File]::ReadAllBytes($tmp)
        # 断言：gofmt 输出若意外带 CR，这里再归一一次，确保写回的是 LF。
        if (Test-HasCr $fixed) { $fixed = ConvertTo-LfBytes $fixed }
        [System.IO.File]::WriteAllBytes($full, $fixed)
        return $true
    }
    finally {
        if (Test-Path $tmp) { Remove-Item $tmp -Force -ErrorAction SilentlyContinue }
    }
}

# ---------------------------------------------------------------------------
# 主流程
# ---------------------------------------------------------------------------

$root = Get-RepoRoot

if (-not (Get-Command gofmt -ErrorAction SilentlyContinue)) {
    Write-Error 'gofmt 不在 PATH 上。请先安装 Go 或把 %GOROOT%\bin 加进 PATH。'
    exit 2
}

if ($Path -and $Path.Count -gt 0) {
    # 宽容解析：既接受 PowerShell 数组（-Path a.go,b.go 在 pwsh 内是数组），
    # 也接受单个逗号串（-Path 'a.go,b.go'，从 pwsh -File 或 CI 调用时常见）。
    # 不做这一步的话，'a.go,b.go' 会被当成一个文件名，静默变成 MISSING。
    $scope = @(
        $Path |
            ForEach-Object { $_ -split ',' } |
            ForEach-Object { $_.Trim() -replace '\\', '/' } |
            Where-Object { $_ -ne '' }
    )
}
else {
    $scope = @(Get-DefaultScope $root $Base)
}

if ($scope.Count -eq 0) {
    Write-Host '没有需要检查的 Go 文件（基准：'"$Base"'）。'
    exit 0
}

$results = @($scope | ForEach-Object { Test-GoFile $root $_ })

if ($Fix) {
    $fixable = @($results | Where-Object { $_.Category -eq 'REAL' })
    foreach ($item in $fixable) {
        if (Invoke-GoFmtFix $root $item.Path) {
            Write-Host ("  已修复（写回 LF）: {0}" -f $item.Path)
        }
    }
    if ($fixable.Count -gt 0) {
        # 修完重新判定，让最终输出反映真实状态（修完应为 CLEAN）。
        $results = @($scope | ForEach-Object { Test-GoFile $root $_ })
    }
}

if ($Json) {
    $payload = [pscustomobject]@{
        checked    = $results.Count
        real       = @($results | Where-Object { $_.Category -eq 'REAL' } | ForEach-Object { $_.Path })
        crlfOnly   = @($results | Where-Object { $_.Category -eq 'CRLF-ONLY' } | ForEach-Object { $_.Path })
        clean      = @($results | Where-Object { $_.Category -eq 'CLEAN' } | ForEach-Object { $_.Path })
        missing    = @($results | Where-Object { $_.Category -eq 'MISSING' } | ForEach-Object { $_.Path })
    }
    # ConvertTo-Json 在 Windows 上输出 CRLF；机器消费方（测试、CI 解析）若按 '\n'
    # 切分会读不到。这里强制成 LF，让输出跨平台字节稳定。
    $jsonText = ($payload | ConvertTo-Json -Depth 4) -replace "`r`n", "`n"
    Write-Output $jsonText
    if ($payload.real.Count -gt 0) { exit 1 }
    exit 0
}

if (-not $Quiet) {
    foreach ($item in $results) {
        switch ($item.Category) {
            'REAL' {
                Write-Host ("REAL      {0}" -f $item.Path) -ForegroundColor Red
                Write-Host ("          {0}" -f $item.Note) -ForegroundColor DarkGray
                if ($item.Diff) {
                    Write-Host $item.Diff -ForegroundColor DarkYellow
                }
            }
            'CRLF-ONLY' {
                Write-Host ("CRLF-ONLY {0}  ({1} 个 CR)" -f $item.Path, $item.CrCount) -ForegroundColor Yellow
                Write-Host ("          {0}" -f $item.Note) -ForegroundColor DarkGray
            }
            'CLEAN' {
                Write-Host ("CLEAN     {0}" -f $item.Path) -ForegroundColor Green
            }
            'MISSING' {
                Write-Host ("MISSING   {0}" -f $item.Path) -ForegroundColor DarkGray
            }
        }
    }
    Write-Host ''
}

$real = @($results | Where-Object { $_.Category -eq 'REAL' })
$crlfOnly = @($results | Where-Object { $_.Category -eq 'CRLF-ONLY' })
$clean = @($results | Where-Object { $_.Category -eq 'CLEAN' })
$missing = @($results | Where-Object { $_.Category -eq 'MISSING' })

Write-Host ("检查 {0} 个文件：REAL={1}  CRLF-ONLY={2}  CLEAN={3}  MISSING={4}" -f `
        $results.Count, $real.Count, $crlfOnly.Count, $clean.Count, $missing.Count)

if ($real.Count -gt 0) {
    Write-Host ''
    Write-Host '存在真格式问题（REAL）：CI 会红。加 -Fix 修复，或手动 gofmt。' -ForegroundColor Red
    exit 1
}

if ($crlfOnly.Count -gt 0) {
    Write-Host ("其中 {0} 个是 CRLF 假阳性：CI 在 LF 下会通过，不需要改动。" -f $crlfOnly.Count) -ForegroundColor Yellow
}
Write-Host '没有真格式问题。' -ForegroundColor Green
exit 0
