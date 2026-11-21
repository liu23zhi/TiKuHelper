// scripts/prepare-web-dist.test.mjs
//
// 钉住 prepare-web-dist.ps1 的两条关键行为（2026-11-19）：
//
//   ① 缺产物时必须 **exit 1**，不能静默跳过。
//      背景：cmd/adapter-service/dist/ 已从 git 删除（ff03630），而 go:embed
//      是编译期读取 —— 缺文件时 go build 依然 exit 0，只是嵌入空目录。如果
//      这个脚本也「宽容」地跳过，整条链路会一路绿到用户手上，变成 503。
//
//   ② 脚本必须**真的把文件同步过去**，而不是只检查源目录存在。
//      判据用「目标目录里出现了源目录的文件」，不是看脚本打印了什么。
//
// 设计要点（防假钉子）：
//   · 断言看 -Json 的结构化输出与真实文件系统状态，不看人类可读文案；
//   · 每个夹具先自证前提成立（用 -SkipBuild，避免依赖 npm 与网络）；
//   · 失败用例同时断言 exit code 与 reason 字段，只断 exit code 的话，
//     「脚本根本没跑起来」也会被当成「正确失败」而白通过。

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, existsSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = dirname(here)
const script = join(here, 'prepare-web-dist.ps1')

function runPwsh(args, cwd) {
  const r = spawnSync('pwsh', ['-NoProfile', '-File', script, ...args], {
    cwd: cwd || repoRoot,
    encoding: 'utf8',
  })
  return { code: r.status, stdout: r.stdout || '', stderr: r.stderr || '' }
}

// 解析 -Json 输出：脚本只打印一行 JSON，但宿主可能混入其它输出，
// 所以取最后一个能解析的大括号块。
function parseJson(stdout) {
  const lines = stdout.split(/\r?\n/).filter((l) => l.trim().startsWith('{'))
  for (let i = lines.length - 1; i >= 0; i -= 1) {
    try {
      return JSON.parse(lines[i])
    } catch {
      // 继续往前找
    }
  }
  return null
}

test('缺 web/dist/index.html 时必须 exit 1，且不静默跳过', () => {
  // 造一个隔离的假仓库：有 scripts/ 与空的 web/，没有任何产物。
  const fake = mkdtempSync(join(tmpdir(), 'prepweb-missing-'))
  try {
    mkdirSync(join(fake, 'scripts'), { recursive: true })
    mkdirSync(join(fake, 'web', 'dist'), { recursive: true })
    mkdirSync(join(fake, 'cmd', 'adapter-service', 'dist'), { recursive: true })
    const fakeScript = join(fake, 'scripts', 'prepare-web-dist.ps1')
    writeFileSync(fakeScript, readFileSync(script))

    const r = spawnSync('pwsh', ['-NoProfile', '-File', fakeScript, '-SkipBuild', '-Json'], {
      cwd: fake, encoding: 'utf8',
    })
    const payload = parseJson(r.stdout || '')
    assert.equal(r.status, 1, `缺产物必须以退出码 1 结束，实际 ${r.status}`)
    assert.ok(payload, `必须输出结构化 JSON，实际: ${r.stdout}`)
    assert.equal(payload.ok, false)
    // ⚠️ 必须断 stage，不能只断 exit code / reason。
    //
    // 实测教训：把「缺 web/dist/index.html 就失败」那一步删掉（改成静默跳过）后，
    // 脚本**仍然** exit 1 —— 因为后面同步完再检查 embed 目录时也会发现没有
    // index.html。只断 exit code 的版本对这次变异完全无感（2/2 全绿）。
    // 断 stage 才能区分「在源头拦住了」和「到最后才发现」。
    assert.equal(payload.stage, 'verify-web-dist',
      `必须在源头（verify-web-dist）就失败，实际卡在 ${payload.stage} —— 说明缺产物被放行了`)
    assert.match(String(payload.reason || ''), /index\.html/, '失败原因必须指明缺 index\.html')
  } finally {
    rmSync(fake, { recursive: true, force: true })
  }
})

test('有产物时必须真的同步到 embed 目录，并报告 ok', () => {
  const fake = mkdtempSync(join(tmpdir(), 'prepweb-ok-'))
  try {
    mkdirSync(join(fake, 'scripts'), { recursive: true })
    mkdirSync(join(fake, 'web', 'dist', 'js'), { recursive: true })
    mkdirSync(join(fake, 'cmd', 'adapter-service', 'dist'), { recursive: true })
    writeFileSync(join(fake, 'scripts', 'prepare-web-dist.ps1'), readFileSync(script))
    // 最小但形状正确的产物
    writeFileSync(
      join(fake, 'web', 'dist', 'index.html'),
      // 必须带上 script 引用：脚本要从 index.html 里解析 app.<hash>.js 的哈希。
      // 第一版夹具漏了这行，于是 indexJs 恒为空 —— 那不是脚本的缺陷，
      // 是夹具形状不对（真实产物一定有这个引用）。
      '<!doctype html><html><head><title>题库</title></head><body>'
        + '<div id="app"></div><script defer src="/js/app.deadbeef.js"></script></body></html>',
    )
    writeFileSync(join(fake, 'web', 'dist', 'js', 'app.deadbeef.js'), 'console.log(1)')
    // 放一个 .gitkeep，验证它不被清掉（目录要靠它留在 git 里）
    writeFileSync(join(fake, 'cmd', 'adapter-service', 'dist', '.gitkeep'), '')

    const fakeScript = join(fake, 'scripts', 'prepare-web-dist.ps1')
    const r = spawnSync('pwsh', ['-NoProfile', '-File', fakeScript, '-SkipBuild', '-Json'], {
      cwd: fake, encoding: 'utf8',
    })
    const payload = parseJson(r.stdout || '')
    assert.equal(r.status, 0, `有产物时必须成功，实际 ${r.status}；stdout=${r.stdout}`)
    assert.ok(payload)
    assert.equal(payload.ok, true)

    // 关键：断言真实文件系统，而不是只看脚本自述。
    const embedDir = join(fake, 'cmd', 'adapter-service', 'dist')
    assert.ok(existsSync(join(embedDir, 'index.html')), 'index.html 必须被复制过去')
    assert.ok(existsSync(join(embedDir, 'js', 'app.deadbeef.js')), 'js 子目录必须被复制过去')
    assert.ok(existsSync(join(embedDir, '.gitkeep')), '.gitkeep 不能被清掉，否则目录会从 git 里消失')
    assert.equal(payload.indexJs, 'deadbeef', '必须正确解析出 app 的哈希')
  } finally {
    rmSync(fake, { recursive: true, force: true })
  }
})
