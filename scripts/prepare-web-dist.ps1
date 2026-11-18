# scripts/prepare-web-dist.ps1
#
# 把前端构建产物同步到 cmd/adapter-service/dist/ —— 也就是 //go:embed all:dist
# 真正读取的那个目录。
#
# 为什么需要这个脚本（2026-11-19）
# ---------------------------------
# cmd/adapter-service/dist/ 已从 git 删除（commit ff03630），仓库里只有 .gitkeep。
# 而 go:embed 是**编译期**读取：没有产物时 go build 依然成功，但打出来的二进制
# 里前端是空的，访问任何页面都返回 503。
#
# 这就是「编译过了、跑起来白屏/503」的经典陷阱：失败发生在运行时，不在构建时。
# 所以凡是**要产出可分发的二进制**的地方（本地手动构建、CI 的 release-build、
# Docker），都必须先跑本脚本。
#
# 用法
# ----
#   pwsh ./scripts/prepare-web-dist.ps1              # 增量：web/dist 比目标新才复制
#   pwsh ./scripts/prepare-web-dist.ps1 -Force       # 强制重建并复制
#   pwsh ./scripts/prepare-web-dist.ps1 -SkipBuild   # 只复制，不跑 npm run build
#   pwsh ./scripts/prepare-web-dist.ps1 -Json        # 输出结构化结果，供测试断言
#
# 退出码
# ------
#   0 = 产物就绪（复制完成或本就最新）
#   1 = 失败（npm 缺失 / 构建失败 / 产物缺 index.html）
#
# 刻意**不**在这里做「静默跳过」：任何一步不成立都必须 exit 1。否则 CI 会在
# 没有前端的情况下继续构建，产出一个 503 二进制而全流程显示成功。

[CmdletBinding()]
param(
    [switch]$Force,
    [switch]$SkipBuild,
    [switch]$Json
)

$ErrorActionPreference = 'Stop'

# 仓库根 = 本脚本所在目录的上一级。用 $PSScriptRoot 而不是相对路径，
# 脚本从任意 cwd 调用都成立（CI 与本地 cwd 不同）。
$repoRoot = Split-Path -Parent $PSScriptRoot
$webDir = Join-Path $repoRoot 'web'
$webDist = Join-Path $webDir 'dist'
$embedDir = Join-Path $repoRoot 'cmd/adapter-service/dist'

function Write-Result {
    param([hashtable]$Payload)
    if ($Json) {
        $Payload | ConvertTo-Json -Compress
    } else {
        foreach ($key in $Payload.Keys) { Write-Host "$key=$($Payload[$key])" }
    }
}

function Fail {
    param([string]$Reason)
    Write-Result @{ ok = $false; reason = $Reason; stage = $script:stage }
    exit 1
}

$script:stage = 'init'

# ── 1. 需要时构建前端 ──────────────────────────────────────────────
# 判据用 mtime：web/src 里任一文件比 web/dist/index.html 新，就认为产物过期。
# 只比 index.html 是够的 —— webpack 每次构建都会重写它，且它一定存在。
$needsBuild = $true
if ($SkipBuild) {
    $needsBuild = $false
} elseif (-not $Force -and (Test-Path $webDist) -and (Test-Path (Join-Path $webDist 'index.html'))) {
    $builtAt = (Get-Item (Join-Path $webDist 'index.html')).LastWriteTimeUtc
    # 分隔符要跨平台：Linux runner 用的是 /，写死 \ 会让过滤在 CI 上失效。
    $sep = [IO.Path]::DirectorySeparatorChar
    $skipDirs = @(
        "${sep}node_modules${sep}",
        "${sep}dist${sep}",
        "${sep}.git${sep}"
    )
    $newest = Get-ChildItem $webDir -Recurse -File -ErrorAction SilentlyContinue |
        Where-Object {
            $path = $_.FullName
            -not ($skipDirs | Where-Object { $path.Contains($_) })
        } |
        Sort-Object LastWriteTimeUtc -Descending |
        Select-Object -First 1
    if ($newest -and $newest.LastWriteTimeUtc -le $builtAt) {
        $needsBuild = $false
    }
}

if ($needsBuild) {
    $script:stage = 'npm-build'
    $npm = Get-Command npm -ErrorAction SilentlyContinue
    if (-not $npm) {
        Fail '找不到 npm。请先安装 Node.js，或改用 -SkipBuild 并提供已有的 web/dist。'
    }
    if (-not (Test-Path (Join-Path $webDir 'package.json'))) {
        Fail "web/package.json 不存在（$webDir）—— 仓库结构不符合预期。"
    }
    Write-Host '构建前端：cd web && npm run build'
    Push-Location $webDir
    try {
        # npm ci 只在没有 node_modules 时跑：CI 每次都是干净环境，本地反复构建
        # 时重装一遍 node_modules 太慢（本项目实测 1000+ 包）。
        if (-not (Test-Path (Join-Path $webDir 'node_modules'))) {
            & npm ci
            if ($LASTEXITCODE -ne 0) { Fail "npm ci 失败（exit $LASTEXITCODE）" }
        }
        & npm run build
        if ($LASTEXITCODE -ne 0) { Fail "npm run build 失败（exit $LASTEXITCODE）" }
    } finally {
        Pop-Location
    }
} elseif ($SkipBuild) {
    # 区分两种「不构建」：用户显式要求跳过 vs 产物本来就新。
    # 合成一句「已是最新」在 -SkipBuild 且产物缺失时会给出误导性提示
    # （后面 verify-web-dist 才报错，看起来像自相矛盾）。
    Write-Host '-SkipBuild：不构建前端，直接使用现有 web/dist'
} else {
    Write-Host '前端产物已是最新，跳过构建'
}

# ── 2. 自证产物成立 ────────────────────────────────────────────────
$script:stage = 'verify-web-dist'
$webIndex = Join-Path $webDist 'index.html'
if (-not (Test-Path $webIndex)) {
    Fail "web/dist/index.html 不存在（$webIndex）—— 构建没有产出前端。"
}

# ── 3. 同步到 embed 目录 ───────────────────────────────────────────
# 先清空目标再复制：webpack 的文件名带内容哈希，旧文件留着会越积越多，
# 而且它们会被一起 embed 进二进制，白白撑大体积。
$script:stage = 'sync'
New-Item -ItemType Directory -Path $embedDir -Force | Out-Null
Get-ChildItem $embedDir -Force -ErrorAction SilentlyContinue |
    Where-Object { $_.Name -ne '.gitkeep' } |
    ForEach-Object { Remove-Item $_.FullName -Recurse -Force }

Copy-Item (Join-Path $webDist '*') $embedDir -Recurse -Force

# ── 4. 复核 embed 目录真的能编出前端 ───────────────────────────────
$script:stage = 'verify-embed'
$embedIndex = Join-Path $embedDir 'index.html'
if (-not (Test-Path $embedIndex)) {
    Fail "复制后 cmd/adapter-service/dist/index.html 仍不存在 —— 同步失败。"
}
$body = Get-Content $embedIndex -Raw
# 两条标记都来自真实构建产物：缺任一条就说明这不是我们认识的前端首页
# （例如复制了别的东西，或构建被中途打断留下了半个文件）。
foreach ($marker in @('id="app"', '<title>')) {
    if ($body -notlike "*$marker*") {
        Fail "index.html 里缺少标记 $marker —— 产物不完整。"
    }
}

$fileCount = (Get-ChildItem $embedDir -Recurse -File).Count
$script:stage = 'done'
Write-Result @{
    ok        = $true
    built     = $needsBuild
    embedDir  = $embedDir
    fileCount = $fileCount
    # 从 index.html 里取 app 的哈希，用于「部署后校验服务端与本地一致」。
    # 同时接受 `src="/js/app.<hash>.js"` 与裸路径两种形态：vue-cli 默认是前者，
    # 但改过 publicPath 后会变成后者（或相对路径），只认一种会静默返回空串。
    indexJs   = if ($body -match 'app\.([0-9a-f]{6,})\.js') { $Matches[1] } else { '' }
}
exit 0
