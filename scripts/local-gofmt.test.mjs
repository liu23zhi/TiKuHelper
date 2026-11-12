// scripts/local-gofmt.test.mjs
//
// 测试 scripts/local-gofmt.ps1 的**分类逻辑**，重点是那个害了 PR#27/#28/#30 三次的
// 「CRLF 假阳性」。判定成败的唯一标准：如果实现坏了，这些断言会不会红。
//
// 设计要点（防假钉子）：
//   1. 夹具用 mkdtemp 造在系统临时目录，不落在仓库里，避免污染 git status。
//   2. 每个夹具都先**自证前提成立**（例如「在 CRLF 下 gofmt -l 确实会报」），
//      否则夹具本身失效时用例会白通过。
//   3. 断言看脚本的 -Json 结构化输出，不看人类可读文本 —— 后者会随措辞调整而碎。
//   4. 第 4 条用例是「三分类自检」：断言 REAL / CRLF-ONLY / CLEAN 在一次运行里
//      都出现过，防止分类逻辑退化成「全都报 REAL」或「全都报 CLEAN」仍能过前三条。

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync, readFileSync, mkdirSync, rmSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const SCRIPT = join(HERE, 'local-gofmt.ps1')

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

// 一个在 LF 下**完全合规**的 Go 文件。
// 用 gofmt -l 自证过：写出来后在 LF 形态下 `gofmt -l` 输出为空。
const CLEAN_GO = `package fixture

func Add(a int, b int) int {
	return a + b
}
`

// 一个在 LF 下**也要被 gofmt 改**的文件：map 字面量键长不同、值未对齐。
// 正是 PR#27 里真实踩到的形态（"banned": userStatusBanned 的列宽）。
const UNFORMATTED_GO = `package fixture

var statusOf = map[string]int{
	"a": 1,
	"bbbb": 2,
}
`

// 物理文件名 → 便于用例内定位
const FILES = {
  cleanLf: 'clean_lf.go',
  cleanCrlf: 'clean_crlf.go',
  realLf: 'real_lf.go',
  realCrlf: 'real_crlf.go',
  mixed: 'mixed_real_crlf.go',
}

function toCrlf(text) {
  return text.replace(/\n/g, '\r\n')
}

// makeFixtureRepo 造一个最小「仓库」：只有 .go 文件，没有 git。
// 脚本通过 -RepoRoot 接收它，因此不需要真的 git init。
function makeFixtureRepo() {
  const root = mkdtempSync(join(tmpdir(), 'local-gofmt-fixture-'))
  // clean：LF 与 CRLF 各一份，内容等价
  writeFileSync(join(root, FILES.cleanLf), CLEAN_GO, 'utf8')
  writeFileSync(join(root, FILES.cleanCrlf), toCrlf(CLEAN_GO), 'utf8')
  // real：LF 与 CRLF 各一份，内容都不合规
  writeFileSync(join(root, FILES.realLf), UNFORMATTED_GO, 'utf8')
  writeFileSync(join(root, FILES.realCrlf), toCrlf(UNFORMATTED_GO), 'utf8')
  // mixed：真问题 + CRLF 同时存在（验证 REAL 优先于 CRLF-ONLY）
  writeFileSync(join(root, FILES.mixed), toCrlf(UNFORMATTED_GO), 'utf8')
  return root
}

// runScript 跑脚本并解析 -Json 输出。
// 用 -Json 是因为断言结构化字段比断言中文文案稳。
function runScript(root, extraArgs = []) {
  const args = [
    '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass',
    '-File', SCRIPT,
    '-RepoRoot', root,
    '-Json',
    ...extraArgs,
  ]
  const result = spawnSync('pwsh', args, { encoding: 'utf8', timeout: 120_000 })
  if (result.error) throw result.error

  const stdout = result.stdout || ''
  const stderr = result.stderr || ''
  // 脚本可能同时打印 Write-Host（进 stdout）与结构化 JSON；取最后一段能 JSON.parse
  // 成功的对象。逐个尝试每个 '{' 起点，避免依赖缩进/行尾风格（PowerShell 的
  // ConvertTo-Json 在 Windows 上曾输出 CRLF，靠 '{\n' 匹配会碎）。
  let payload = null
  for (let i = stdout.length - 1; i >= 0; i--) {
    if (stdout[i] !== '{') continue
    try {
      payload = JSON.parse(stdout.slice(i))
      break
    } catch {
      /* 继续往前找 */
    }
  }
  assert.ok(payload, `脚本未输出可解析的 JSON。stdout=${stdout}\nstderr=${stderr}`)
  return { payload, status: result.status, stdout, stderr }
}

// gofmtList 直接调 gofmt -l 自证夹具前提（不依赖被测脚本，避免循环论证）。
function gofmtList(filePath) {
  const r = spawnSync('gofmt', ['-l', filePath], { encoding: 'utf8' })
  if (r.error) throw r.error
  return (r.stdout || '').trim()
}

function cleanup(root) {
  if (root && existsSync(root)) rmSync(root, { recursive: true, force: true })
}

// ---------------------------------------------------------------------------
// 用例 1：CRLF 假阳性必须报 CRLF-ONLY，不能报 REAL
// ---------------------------------------------------------------------------
test('CRLF false positive is classified CRLF-ONLY, not REAL', () => {
  const root = makeFixtureRepo()
  try {
    // ① 先自证夹具前提：同一内容在 CRLF 下 gofmt -l 确实会报。
    //    没有这一步，如果 gofmt 未来改了行为（不再因 CRLF 报错），
    //    本用例会在「假阳性根本不存在」的情况下白通过。
    const crlfListed = gofmtList(join(root, FILES.cleanCrlf))
    assert.notEqual(crlfListed, '',
      'FIXTURE INVALID: gofmt -l 未对 CRLF 文件报警，本用例的前提不成立（无法验证假阳性分类）')

    // ② 同一内容的 LF 版本必须合规，否则这不是「假阳性」而是真问题。
    const lfListed = gofmtList(join(root, FILES.cleanLf))
    assert.equal(lfListed, '',
      'FIXTURE INVALID: LF 版本也不合规，说明夹具不是「假阳性」样本')

    // ③ 真正的断言：脚本必须把它判成 CRLF-ONLY。
    const { payload } = runScript(root, ['-Path', FILES.cleanCrlf])
    assert.deepEqual(payload.crlfOnly, [FILES.cleanCrlf],
      `CRLF 假阳性必须归入 crlfOnly，实际 crlfOnly=${JSON.stringify(payload.crlfOnly)}`)
    assert.deepEqual(payload.real, [],
      `CRLF 假阳性不能被报成 REAL，实际 real=${JSON.stringify(payload.real)}`)
  } finally {
    cleanup(root)
  }
})

// ---------------------------------------------------------------------------
// 用例 2：真格式问题必须报 REAL（且 LF / CRLF 两种形态都要能识别）
// ---------------------------------------------------------------------------
test('genuinely unformatted file is classified REAL in both LF and CRLF form', () => {
  const root = makeFixtureRepo()
  try {
    // 自证前提：这个夹具在 LF 下确实需要格式化。
    const lfListed = gofmtList(join(root, FILES.realLf))
    assert.notEqual(lfListed, '',
      'FIXTURE INVALID: 夹具在 LF 下也合规，无法验证 REAL 分类')

    // 真正的断言：LF 形态与 CRLF 形态都必须报 REAL。
    // CRLF 形态这一条尤其重要 —— 它证明脚本没有被换行干扰成 CRLF-ONLY。
    const { payload } = runScript(root, ['-Path', `${FILES.realLf},${FILES.realCrlf}`])
    assert.deepEqual(payload.real.sort(), [FILES.realCrlf, FILES.realLf].sort(),
      `两个形态都应报 REAL，实际 real=${JSON.stringify(payload.real)}`)
    assert.deepEqual(payload.crlfOnly, [],
      `真问题不能被降级成 CRLF-ONLY（那会让人以为 CI 会绿），实际 crlfOnly=${JSON.stringify(payload.crlfOnly)}`)
  } finally {
    cleanup(root)
  }
})

// ---------------------------------------------------------------------------
// 用例 3：干净文件报 CLEAN
// ---------------------------------------------------------------------------
test('formatted LF file is classified CLEAN', () => {
  const root = makeFixtureRepo()
  try {
    // 自证前提
    assert.equal(gofmtList(join(root, FILES.cleanLf)), '',
      'FIXTURE INVALID: 夹具本身不合规')

    const { payload } = runScript(root, ['-Path', FILES.cleanLf])
    assert.deepEqual(payload.clean, [FILES.cleanLf],
      `合规文件必须归入 clean，实际 clean=${JSON.stringify(payload.clean)}`)
    assert.deepEqual(payload.real, [])
    assert.deepEqual(payload.crlfOnly, [],
      '合规文件不能被误报成 CRLF-ONLY（它压根没有 CR）')
  } finally {
    cleanup(root)
  }
})

// ---------------------------------------------------------------------------
// 用例 4：三分类自检 —— 一次运行里三种分类都必须出现
//
// 这条是防假钉子的核心：前三条各自只验一个分类，如果实现退化成
// 「无条件报 REAL」或「无条件报 CLEAN」，单看某一条仍可能过。这里要求
// 同一批输入同时产出三类结果，分类逻辑才有意义。
// ---------------------------------------------------------------------------
test('all three categories appear in one run (guards against degenerate classifiers)', () => {
  const root = makeFixtureRepo()
  try {
    const { payload } = runScript(root, [
      '-Path',
      [FILES.cleanLf, FILES.cleanCrlf, FILES.realLf].join(','),
    ])

    assert.equal(payload.checked, 3, '应检查 3 个文件')
    assert.deepEqual(payload.clean, [FILES.cleanLf],
      `CLEAN 应恰好是 cleanLf，实际 ${JSON.stringify(payload.clean)}`)
    assert.deepEqual(payload.crlfOnly, [FILES.cleanCrlf],
      `CRLF-ONLY 应恰好是 cleanCrlf，实际 ${JSON.stringify(payload.crlfOnly)}`)
    assert.deepEqual(payload.real, [FILES.realLf],
      `REAL 应恰好是 realLf，实际 ${JSON.stringify(payload.real)}`)

    // 显式自检：三类都非空。即使上面的 deepEqual 全过，这一条也直白表达意图。
    assert.ok(payload.clean.length > 0 && payload.crlfOnly.length > 0 && payload.real.length > 0,
      `三种分类都必须出现过，实际 clean=${payload.clean.length} crlfOnly=${payload.crlfOnly.length} real=${payload.real.length}`)
  } finally {
    cleanup(root)
  }
})

// ---------------------------------------------------------------------------
// 用例 5：REAL 优先于 CRLF-ONLY —— 真问题 + CRLF 同时存在时不能只报 CRT-ONLY
//
// 这是 PR#27 的真实形态：文件既有对齐问题、又是 CRLF。如果分类写成
// 「有 CR 就先当假阳性」，这个文件会被放过去，CI 照样红。
// ---------------------------------------------------------------------------
test('file that is both CRLF and unformatted is REAL, not CRLF-ONLY', () => {
  const root = makeFixtureRepo()
  try {
    const { payload } = runScript(root, ['-Path', FILES.mixed])
    assert.deepEqual(payload.real, [FILES.mixed],
      `既是 CRLF 又不合规的文件必须报 REAL，实际 real=${JSON.stringify(payload.real)}`)
    assert.deepEqual(payload.crlfOnly, [],
      `不能因为有 CR 就降级成 CRLF-ONLY，实际 crlfOnly=${JSON.stringify(payload.crlfOnly)}`)
  } finally {
    cleanup(root)
  }
})

// ---------------------------------------------------------------------------
// 用例 6：-Fix 写回必须是 LF，不能写回 CRLF
//
// 若 -Fix 写回 CRLF，修完再跑仍会报（gofmt -l 对 CRLF 报警），进入死循环。
// 这条直接断言字节里没有 CR。
// ---------------------------------------------------------------------------
test('-Fix writes back LF only, never CRLF', () => {
  const root = makeFixtureRepo()
  try {
    const target = join(root, FILES.realCrlf)
    // 前提：这个文件确实含 CR。
    assert.ok(readFileSync(target).includes(13),
      'FIXTURE INVALID: 夹具本身不含 CR，无法验证 -Fix 是否会把换行写回 CRLF')

    const { status } = runScript(root, ['-Path', FILES.realCrlf, '-Fix'])
    assert.equal(status, 0, '-Fix 修复后应退出 0')

    const fixed = readFileSync(target)
    assert.equal(fixed.includes(13), false,
      `-Fix 写回后仍含 CR —— 换行被还原成 CRLF 了（会导致下次 gofmt -l 继续报警）`)

    // 修完之后应判为 CLEAN（不是 REAL，也不是 CRLF-ONLY）。
    const after = runScript(root, ['-Path', FILES.realCrlf])
    assert.deepEqual(after.payload.clean, [FILES.realCrlf],
      `-Fix 之后应判 CLEAN，实际 clean=${JSON.stringify(after.payload.clean)} real=${JSON.stringify(after.payload.real)}`)
  } finally {
    cleanup(root)
  }
})

// ---------------------------------------------------------------------------
// 用例 7：CRLF-ONLY 时退出码为 0（CI 会绿，不该让本地 pre-push 失败）
// ---------------------------------------------------------------------------
test('CRLF-ONLY alone yields exit code 0, REAL yields 1', () => {
  const root = makeFixtureRepo()
  try {
    const onlyCrlf = runScript(root, ['-Path', FILES.cleanCrlf])
    assert.equal(onlyCrlf.status, 0,
      'CRLF-ONLY 时退出码应为 0（CI 会通过，不该阻断本地流程）')

    const withReal = runScript(root, ['-Path', FILES.realLf])
    assert.equal(withReal.status, 1,
      'REAL 时退出码应为 1（与 CI 语义对齐）')
  } finally {
    cleanup(root)
  }
})
