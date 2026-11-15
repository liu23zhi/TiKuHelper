import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const userListPagePath = path.join(__dirname, '..', 'pages', 'UserList', 'index.vue')
const bindingApiPath = path.join(__dirname, '..', 'api', 'UserBindingApi.js')

// 「显示完整值」按钮 + 审计（用户 2026-11-15 拍板。
// 原话：「加一个「显示完整值」按钮并记审计日志，而不是默认明文。」）
//
// 这些用例守的是四条容易被「看起来对」的实现架空的语义：
//  1. **默认仍然脱敏** —— 不能有人为了省事把 value 直接下发到列表里；
//  2. **只有单条** —— 接口与页面都不得出现批量揭示；
//  3. **明文只在弹窗存活期内存在** —— 打开/重载/关闭三处都要重置，
//     漏一处就是「换了用户还显示上一个用户的手机号」；
//  4. **失败必须可见** —— 403/404/网络错误都要 message.error，
//     静默会让管理员以为「这个号没有绑定值」。
//
// 剥注释后再断言：本仓踩过「注释里写着反例，doesNotMatch 假红」的坑
// （见 t200）。所以凡 doesNotMatch 都先剥 /* */ 与 //。

function stripComments(source) {
  return source
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\/\/[^\n]*/g, '')
}

async function readPage() {
  return readFile(userListPagePath, 'utf8')
}

test('绑定值列：默认脱敏，标题不再写「（脱敏）」', async () => {
  const source = await readPage()
  const code = stripComments(source)

  // 标题改为「绑定值」：同一列里已有解读入口，还写「（脱敏）」会与用户看到的行为矛盾。
  assert.match(code, /title: '绑定值', dataIndex: 'valueMasked', key: 'valueMasked'/)
  assert.doesNotMatch(code, /绑定值（脱敏）/, '列标题不应再带「（脱敏）」')

  // 默认态渲染的是 valueMasked；明文只从揭示缓存里取。
  assert.match(code, /class="binding-masked-cell">\{\{ record\.valueMasked \|\| '—' \}\}/)
  assert.match(code, /class="binding-revealed-cell">\{\{ bindingRevealedValue\(record\.id\) \|\| '—' \}\}/)
})

test('绑定值列：三态按钮（显示完整值 / loading / 已显示且禁用）', async () => {
  const source = await readPage()
  const code = stripComments(source)

  // 未揭示：可点击，文案「显示完整值」。
  assert.match(code, /@click="revealBinding\(record\)"/)
  assert.match(code, />显示完整值<\/a-button>/)

  // 「已显示」那个按钮必须在**它自己的标签内**带 disabled。
  //
  // ⚠️ 不能用「已显示 之后全文出现 disabled」来断言：相邻的「显示完整值」按钮
  // 也有 :disabled（它是动态的），全文匹配会被那一处满足 —— 实测把「已显示」的
  // disabled 删掉后用例仍然全绿（假钉子）。
  // 因此这里按属性顺序切出「已显示」那个 <a-button ...> 标签再判。
  const shownButtonTag = code.match(/<a-button[\s\S]{0,400}?data-testid="binding-reveal-shown"[\s\S]{0,200}?>/)
  assert.ok(shownButtonTag, '「已显示」按钮必须带 data-testid="binding-reveal-shown" 便于定位')
  const tag = shownButtonTag[0]
  assert.ok(/(^|\s)disabled(\s|$)/m.test(tag.replace(/<a-button/, '')),
    `「已显示」按钮必须静态 disabled（重复点击会重复写审计），实际标签：${tag}`)
  assert.ok(tag.includes('>已显示</a-button>') || code.includes('>已显示</a-button>'),
    '文案必须是「已显示」')

  // 揭示中：loading + disabled，挡住连点。
  assert.match(code, /:loading="isBindingRevealPending\(record\.id\)"/)
  assert.match(code, /:disabled="isBindingRevealPending\(record\.id\)"/)
})

test('绑定值揭示：本地 state 用 Map<bindingId, plaintext>', async () => {
  const source = await readPage()
  const code = stripComments(source)

  // Map 而不是普通对象：`has` 判定不依赖 truthiness —— 一个空字符串的绑定值
  // 也必须算「已揭示」，否则按钮一直可点、每点一次多一条审计。
  assert.match(code, /const bindingRevealValues = ref\(new Map\(\)\)/)
  assert.match(code, /const bindingRevealPendingIds = ref\(new Set\(\)\)/)
  assert.match(code, /return bindingRevealValues\.value\.has\(key\)/)
  // 揭示成功后写入 Map（而不是写回 bindingsRows 的行对象）。
  assert.match(code, /values\.set\(id, row\.value\)/)
  assert.doesNotMatch(code, /bindingsRows\.value[^\n]*\.value\s*=/, '明文不得写回列表行对象')
})

test('绑定值揭示：打开 / 重载 / 关闭三处都重置为脱敏态', async () => {
  const source = await readPage()
  const code = stripComments(source)

  // 三个调用点缺一不可；只重置其中一处的后果是「换用户后仍显示上一个用户的明文」。
  const callSites = code.match(/ctx\.resetBindingReveals\(\)/g) || []
  assert.equal(callSites.length, 3, `resetBindingReveals 必须在 open/reload/close 各调用一次，实际 ${callSites.length} 次`)

  // 逐个定位：openBindingsModal / reloadBindings / closeBindingsModal。
  const openAt = code.indexOf('async openBindingsModal(record)')
  const reloadAt = code.indexOf('async reloadBindings()')
  const closeAt = code.indexOf('closeBindingsModal()')
  assert.ok(openAt > 0 && reloadAt > openAt && closeAt > reloadAt, '三个方法的相对顺序变了，请复核断言')
  for (const [label, start, end] of [
    ['openBindingsModal', openAt, reloadAt],
    ['reloadBindings', reloadAt, closeAt],
    ['closeBindingsModal', closeAt, closeAt + 600],
  ]) {
    const body = code.slice(start, end)
    assert.ok(body.includes('resetBindingReveals()'),
      `${label} 必须调用 resetBindingReveals()（否则换用户/刷新/关窗后明文会残留）`)
  }
})

test('绑定值揭示：失败必须给出可见提示，且重复点击不再请求', async () => {
  const source = await readPage()
  const code = stripComments(source)

  // 定位 revealBinding 方法体（到下一个方法定义为止），避免用「窗口内出现
  // message.error」这种弱断言 —— 实测它会被相邻分支的 message.error 满足，
  // 删掉 404 那一句时测试仍然全绿（假钉子）。
  const start = code.indexOf('async revealBinding(record)')
  assert.ok(start > 0, 'revealBinding 方法必须存在')
  const nextMethod = code.indexOf('\n    openBalanceModal()', start)
  const body = code.slice(start, nextMethod > start ? nextMethod : start + 3000)

  // 三条失败路径各自带着**可区分**的公开文案（静默会把「没权限」显示成「没有绑定值」）。
  assert.ok(body.includes("message.error('无权限查看绑定值，请确认账号权限后再操作。')"),
    '403 必须有专属提示（不能与一般失败混用同一句）')
  assert.ok(body.includes("message.error('该绑定记录已不存在，请重新打开弹窗刷新列表。')"),
    '404 必须有专属提示（记录被删与没权限是两件事）')
  assert.ok(body.includes('status !== 200'), '未知状态码必须单独判失败')
  assert.ok(body.includes("message.error('绑定值读取未完成，请稍后重试。')"),
    '网络/其它失败必须有提示')

  // 方法体内的 message.error 恰好 4 处：403 / 404 / 非 200 / catch。
  // 少一处就意味着某条失败路径静默（上面的逐句断言已覆盖具体文案，
  // 这一条防「新增一条 else 分支但没有提示」）。
  const calls = body.match(/message\.error\(/g) || []
  assert.equal(calls.length, 4, `revealBinding 内应有 4 处 message.error（403/404/非200/catch），实际 ${calls.length}`)

  // 幂等：已揭示 / 在途 都直接 return，不发请求（后端每次调用都会写审计）。
  assert.ok(body.includes('if (ctx.isBindingRevealed(id)) return'),
    '已揭示必须直接 return（重复请求会重复写审计）')
  assert.ok(body.includes('if (ctx.isBindingRevealPending(id)) return'),
    '在途必须直接 return（loading 期间连点要挡住）')

  // finally 必须清掉 pending，否则一次失败会把该行永久卡在 loading。
  assert.match(body, /finally \{[\s\S]{0,200}remaining\.delete\(id\)/)
})

test('揭示接口只有单条形态，不存在批量入口', async () => {
  const api = await readFile(bindingApiPath, 'utf8')
  const code = stripComments(api)

  // 单条 URL：/api/admin/bindings/:id/reveal（id 是 binding record 主键）。
  assert.match(code, /url: `\/api\/admin\/bindings\/\$\{value\}\/reveal`/)
  assert.match(code, /method: 'get'/)
  // 参数校验：非法 id 在拼 URL 之前就要抛。
  assert.match(code, /if \(!Number\.isInteger\(value\) \|\| value <= 0\) throw new Error\('绑定记录参数错误'\)/)

  // 反向钉子：不得提供批量揭示（ids 数组 / 列表返明文 / bulk 命名）。
  assert.doesNotMatch(code, /revealAllBinding|revealBindings|bulkReveal/i, '不得提供批量揭示入口')
  assert.doesNotMatch(code, /params:\s*\{[^}]*ids/, '不得通过 ids 参数批量揭示')
  // 导出清单里只允许这一个揭示函数。
  const exported = code.match(/export function (\w*[Rr]eveal\w*)/g) || []
  assert.deepEqual(exported, ['export function revealBindingValue'],
    `揭示相关导出只应有 revealBindingValue，实际 ${JSON.stringify(exported)}`)
})
