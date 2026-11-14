import test from 'node:test'
import assert from 'node:assert/strict'
import {
  buildScriptDomainFromFormat,
  buildScriptDomainForms,
  normalizeScriptDomainFormats,
  parseScriptDomainFormat,
  splitScriptDomainForFormat,
  validateScriptDomainSlot,
} from './scriptDomainFormats.mjs'

// 脚本域名格式（诉求 ④ / ⑤）。
//
// 与后端 internal/service/script_domain_format_test.go 一一对应。两处必须同源：
// 若只改一边，表现是「界面能填但后端拒绝」或「界面显示 A、实际存成 B」。
//
// 三个必须显式表态的语义决策（captain 要求写清）：
//   Q2 `*.a.com` **不匹配** `a.com` 本身 —— 严格语义，裸域另配一条 `a.com/*`
//   Q3 `a.com/*` **匹配多层路径**（`a.com/x/y` 命中），但**不匹配**根路径
//   Q1 旧格式在这里返回 null（不静默当成有效值），由调用方报错

test('Q1：旧格式一律解析为 null，绝不悄悄当成有效规则', () => {
  const legacy = [
    'a.com',            // 裸域名（上一版主写法）
    'a.com/a/*',        // 段级 glob
    'a.com/**',         // 多段 glob
    'https://a.com',    // 带 scheme
    'a.com/a/**/x',
    '*.a.com/',         // 尾斜杠
    'a.com/a.com',
  ]
  for (const raw of legacy) {
    assert.equal(parseScriptDomainFormat(raw), null, `${raw} 必须被拒绝`)
  }
})

test('新语法只有两种形态，且 slot 位置由种类决定', () => {
  assert.deepEqual(parseScriptDomainFormat('*.a.com'), { raw: '*.a.com', host: 'a.com', kind: 'wildcard', slot: 'prefix' })
  assert.deepEqual(parseScriptDomainFormat('a.com/*'), { raw: 'a.com/*', host: 'a.com', kind: 'path', slot: 'suffix' })
  // 归一化：大小写与前后空白
  assert.equal(parseScriptDomainFormat('  *.A.COM  ').raw, '*.a.com')
  // 畸形形态
  for (const raw of ['a.*.a.com', '**.a.com', '*a.com', '*.com', '*.a', '*.a.1', '*.-a.com', '*.a..com', '*.a.com.', '', '   ']) {
    assert.equal(parseScriptDomainFormat(raw), null, `${raw} 必须被拒绝`)
  }
})

test('normalizeScriptDomainFormats 去非法、去重、排序', () => {
  assert.deepEqual(
    normalizeScriptDomainFormats([' *.A.COM ', 'a.com/*', '*.a.com', 'b.com', '  ']),
    ['*.a.com', 'a.com/*'],
  )
})

test('Q2：*.a.com 的 slot 拼在**前面**，且只接受子域内容', () => {
  assert.equal(buildScriptDomainFromFormat('*.a.com', 'x'), 'x.a.com')
  assert.equal(buildScriptDomainFromFormat('*.a.com', 'x.y'), 'x.y.a.com')
  // ⚠️ 变异判据：把拼装顺序改成 `${host}.${slot}` 会让上面两条红。
  for (const slot of ['', '.x', 'x.', 'a..b', '-x', 'x-', 'x/y', 'x y', 'x*']) {
    assert.equal(buildScriptDomainFromFormat('*.a.com', slot), '', `slot=${slot} 必须被拒绝`)
  }
})

test('Q3：a.com/* 的 slot 拼在**后面**，且允许多层路径', () => {
  assert.equal(buildScriptDomainFromFormat('a.com/*', 'p'), 'a.com/p')
  assert.equal(buildScriptDomainFromFormat('a.com/*', 'p/q'), 'a.com/p/q')
  assert.equal(buildScriptDomainFromFormat('a.com/*', 'p/q/r'), 'a.com/p/q/r')
  // 前后斜杠被归一化掉
  assert.equal(buildScriptDomainFromFormat('a.com/*', '/p/q/'), 'a.com/p/q')
  for (const slot of ['', ' ', 'p//q', '../etc', 'p/./q', 'p/../q', 'p*q', 'p q', 'p\\q', 'p?x', 'p#y']) {
    assert.equal(buildScriptDomainFromFormat('a.com/*', slot), '', `slot=${slot} 必须被拒绝`)
  }
})

test('validateScriptDomainSlot 给出的错误文案指向具体原因', () => {
  // 不能只有一句「请填写完整」——用户要能照着改。
  assert.match(validateScriptDomainSlot('wildcard', ''), /填写/)
  assert.match(validateScriptDomainSlot('wildcard', '.x'), /点只能出现在标签之间/)
  assert.match(validateScriptDomainSlot('wildcard', 'x/y'), /字母、数字与连字符/)
  assert.match(validateScriptDomainSlot('path', 'p/../q'), /\. 或 \.\. 段/)
  assert.match(validateScriptDomainSlot('path', 'p*q'), /不能包含/)
  assert.equal(validateScriptDomainSlot('wildcard', 'x'), '')
  assert.equal(validateScriptDomainSlot('path', 'p/q'), '')
})

test('多条格式并存时都能生成（诉求 ⑤「一个脚本可以有多个域名格式」）', () => {
  const result = buildScriptDomainForms([
    { format: '*.a.com', slot: 'x' },
    { format: 'a.com/*', slot: 'p/q' },
  ])
  assert.deepEqual(result.domains, ['x.a.com', 'a.com/p/q'])
  assert.deepEqual(result.errors, [])
})

test('多条格式的错误必须指出是哪一行，不能只说「填写不完整」', () => {
  const result = buildScriptDomainForms([
    { format: '*.a.com', slot: 'ok' },
    { format: 'a.com/*', slot: '' },
    { format: '', slot: 'x' },
  ])
  assert.deepEqual(result.domains, ['ok.a.com'])
  assert.equal(result.errors.length, 2)
  assert.equal(result.errors[0].index, 1)
  assert.match(result.errors[0].message, /填写/)
  assert.equal(result.errors[1].index, 2)
  assert.match(result.errors[1].message, /请选择域名格式/)
})

test('split 是 build 的逆运算（UI 回填编辑态）', () => {
  const cases = [
    ['*.a.com', 'x.a.com', 'x'],
    ['*.a.com', 'x.y.a.com', 'x.y'],
    ['a.com/*', 'a.com/p', 'p'],
    ['a.com/*', 'a.com/p/q', 'p/q'],
  ]
  for (const [format, domain, wantSlot] of cases) {
    assert.equal(splitScriptDomainForFormat(format, domain), wantSlot, `${format} + ${domain}`)
    assert.equal(buildScriptDomainFromFormat(format, wantSlot), domain, 'roundtrip')
  }
  // 不匹配的域名返回 null，不能返回一个看似合法的槽位
  for (const domain of ['a.com', 'b.com', 'x.a.com.evil.cn', '']) {
    assert.equal(splitScriptDomainForFormat('*.a.com', domain), null, domain)
  }
  // Q2 的另一种表述：裸域不是 `*.a.com` 的槽位
  assert.equal(splitScriptDomainForFormat('*.a.com', 'a.com'), null)
})
