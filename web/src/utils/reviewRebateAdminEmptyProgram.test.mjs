import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// 「零活动」被误判成「加载失败」的回归钉子。
//
// # 缺陷形态（用户看到的）
//
// 进「评价返现管理」页面 → 红色告警「活动配置尚未加载成功，请重试读取」。
// 管理员反复点重试，永远一样。
//
// # 真根因
//
// 后端 `GET /api/admin/review-rebate/programs` 在**表为空**时合法返回
// `{data:[]}` + 200（service.ListReviewRebateProgramsWithDB 用 make(...,0)
// 初始化，注释写明「空列表必须是 []，不是 null」）。
// 前端 applyProgram 把它解成 `data.data[0]` = undefined → 抛
// `invalid program payload` → 落进 catch → `programFailure` 被置上 →
// 页面显示「尚未加载成功」。
//
// **配置没错，只是还没建过活动。** 全新部署必然命中。
//
// # 为什么用「截取源码区段执行」而不是断言源码里有没有某个字符串
//
// 字符串断言只能证明「这行代码存在」，不能证明 applyProgram 真的**不再抛错**。
// 若实现改成 `if (empty) throw` 而字符串仍在，断言照绿 —— 那就是假钉子。
// 所以这里按起止标记把 applyProgram 的原文切出来执行，直接看返回值/副作用。

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)
const adminPagePath = path.join(__dirname, '..', 'pages', 'ReviewRebateAdmin', 'index.vue')

// 起止标记：applyProgram 前的注释块 + 函数体结束。
//
// 终点用「函数结束的 `programMissing.value = false` + 紧随的 programLoaded 行 +
// 闭合花括号」这段唯一序列。不写死行尾（仓库混用 CRLF/LF），改用正则定位。
const REGION_START = 'const isProgramMissing = (payload) => {'
const REGION_END_RE = /programMissing\.value = false[\r\n]+\s*programLoaded\.value = true[\r\n]+\s*\}/

// countOccurrences 用 indexOf 循环计数（不用 split —— split 会把匹配串吃掉，
// 连续两次出现时计数偏少，正是「标记被复制」这种最阴的形态该被抓住的地方）。
function countOccurrences(haystack, needle) {
  let count = 0
  let from = 0
  for (;;) {
    const at = haystack.indexOf(needle, from)
    if (at < 0) return count
    count += 1
    from = at + needle.length
  }
}

async function loadAdminPage() {
  return readFile(adminPagePath, 'utf8')
}

// extractRegion 切出「从 isProgramMissing 定义到 applyProgram 收尾」的原文。
//
// 四道自检（缺一条就可能静默失真，见项目记忆「源码标记截取执行型测试的
// 静默失败面」）：标记各出现一次、end > start、区段非空、区段在 <script> 内。
function extractRegion(source) {
  assert.equal(countOccurrences(source, REGION_START), 1,
    `起点标记必须恰好出现 1 次（被删或被复制都会让本测试执行到失效副本），实际 ${countOccurrences(source, REGION_START)} 次`)
  const endMatches = [...source.matchAll(new RegExp(REGION_END_RE.source, 'g'))]
  assert.equal(endMatches.length, 1,
    `终点标记必须恰好出现 1 次，实际 ${endMatches.length} 次`)
  const start = source.indexOf(REGION_START)
  const endMatch = endMatches[0]
  assert.ok(endMatch.index > start, '终点必须在起点之后')
  // 收尾的闭合花括号本身也要包含进来，否则切出的区段是个残缺的函数体。
  const region = source.slice(start, endMatch.index + endMatch[0].length)
  assert.ok(region.trim().length > 0, '切出的区段不能为空')
  const scriptOpen = source.indexOf('<script>')
  const scriptClose = source.lastIndexOf('</script>')
  assert.ok(scriptOpen > 0 && scriptClose > scriptOpen, '<script> 区块必须存在')
  assert.ok(start > scriptOpen && start < scriptClose, '区段必须位于 <script> 区块内')
  return region
}

// buildApplyProgram 把区段原文包成可执行函数，注入它依赖的外部符号。
//
// 注入的 readNonnegativeNumber / emptyProgramForm / programForm / refs 与页面里的
// 真实实现同形；被测的核心逻辑（空数组判定与提前 return）是**源码原文**，
// 所以改坏源码会真的影响这里的执行结果。
function buildApplyProgram(region) {
  const refs = () => ({ value: null })
  const programForm = {
    name: 'stale', storeUrl: 'stale', extensionId: 'stale', enabled: true,
    maxClaimsPerUser: 99, maxAmountPerUser: 99, minAgentConfidence: 0.99, policyVersion: 'stale',
  }
  const state = {
    loadedProgram: refs(),
    programLoaded: refs(),
    programMissing: refs(),
  }
  const emptyProgramForm = () => ({
    name: '', storeUrl: '', extensionId: '', enabled: false,
    maxClaimsPerUser: 3, maxAmountPerUser: 5.04, minAgentConfidence: 0.6, policyVersion: '',
  })
  const readNonnegativeNumber = (value) => {
    if (!['number', 'string'].includes(typeof value) || String(value).trim() === '') return null
    const number = Number(value)
    return Number.isFinite(number) && number >= 0 ? number : null
  }

  // 区段使用了 `const isProgramMissing = ...` 与 `function applyProgram(...)`，
  // 直接拼进函数体会变成嵌套声明（在块里合法但外部看不到），所以显式 return 出去。
  const factory = new Function('loadedProgram', 'programLoaded', 'programMissing',
    'programForm', 'emptyProgramForm', 'readNonnegativeNumber',
    `${region}\n      return { isProgramMissing, applyProgram }`)
  const built = factory(state.loadedProgram, state.programLoaded, state.programMissing,
    programForm, emptyProgramForm, readNonnegativeNumber)
  assert.equal(typeof built.applyProgram, 'function', '区段执行后必须导出 applyProgram（区段被改坏时这里会先失败）')
  assert.equal(typeof built.isProgramMissing, 'function', '区段执行后必须导出 isProgramMissing')
  return { ...built, programForm, state }
}

test('零活动（{data:[]}）必须被识别为「尚未创建」，而不是解析失败', async () => {
  const region = extractRegion(await loadAdminPage())
  const { isProgramMissing } = buildApplyProgram(region)

  // 后端在空表时返回的正是这个形状（gin.H{"data": programs}，programs 为 []）。
  assert.equal(isProgramMissing({ data: [] }), true, '{data:[]} 是「没有活动」，不是错误')
  // 兼容裸数组形状（历史/直连调用）。
  assert.equal(isProgramMissing([]), true, '裸空数组同样是「没有活动」')
  // 反向：有数据时不能误判成 missing，否则真实配置会被当成「没建过」而清空表单。
  assert.equal(isProgramMissing({ data: [{ id: 7, name: 'active' }] }), false, '有活动时不得判为 missing')
  assert.equal(isProgramMissing([{ id: 7 }]), false, '裸数组有元素时不得判为 missing')
  // 非列表形状（{program:{...}} 等）走既有解包路径，不算 missing。
  assert.equal(isProgramMissing({ program: { id: 7 } }), false)
  assert.equal(isProgramMissing({}), false)
})

test('零活动时 applyProgram 不抛错，清空表单并把 programLoaded 置 true', async () => {
  const region = extractRegion(await loadAdminPage())
  const { applyProgram, programForm, state } = buildApplyProgram(region)

  // 关键：这行在原实现里会抛 `invalid program payload`。
  assert.doesNotThrow(() => applyProgram({ data: [] }),
    '零活动不得抛错 —— 抛错就会显示「活动配置尚未加载成功」')

  // programLoaded 必须为 true：数据确实读到了，只是内容为空。
  // 若为 false，页面会一直显示「尚未加载成功」并挡住创建入口。
  assert.equal(state.programLoaded.value, true, '零活动时 programLoaded 必须置 true（数据读到了）')
  assert.equal(state.programMissing.value, true, '零活动时 programMissing 必须置 true')
  assert.equal(state.loadedProgram.value, null, '零活动时不得残留上一家的 program 对象（避免带着旧 id 去 PUT）')

  // 表单必须被重置成空配置：否则「保存」会把上一家的值当成本次创建内容。
  assert.equal(programForm.name, '', '零活动时表单必须清空')
  assert.equal(programForm.enabled, false, '零活动时活动开关必须为关闭')
  assert.equal(programForm.maxClaimsPerUser, 3, '零活动时回落到默认次数')
  assert.equal(programForm.maxAmountPerUser, 5.04, '零活动时回落到默认金额')
})

test('有活动时 applyProgram 正常套用，且不残留 missing 标记', async () => {
  const region = extractRegion(await loadAdminPage())
  const { applyProgram, programForm, state } = buildApplyProgram(region)

  applyProgram({ data: [{ id: 42, name: '商店评价', enabled: true, maxClaimsPerUser: 5, storeUrl: 'https://x.invalid' }] })

  assert.equal(state.programLoaded.value, true)
  assert.equal(state.programMissing.value, false, '有活动时 missing 必须归位，否则会误显示「尚未创建」')
  assert.equal(state.loadedProgram.value.id, 42, '必须保留活动 id（保存时要 PUT /programs/:id）')
  assert.equal(programForm.name, '商店评价')
  assert.equal(programForm.maxClaimsPerUser, 5)
})

test('保存路径在零活动时给出「缺接口」的明确提示，而不是 MALFORMED_RESPONSE', async () => {
  const source = await loadAdminPage()

  // 原实现在没有 id 时报 ensureRequestFailure({code:'MALFORMED_RESPONSE'})，
  // 那会让管理员以为后端返回了坏数据，实际是**后端根本没有创建接口**
  // （service.CreateReviewRebateProgramWithDB 全仓零调用方，router 只有 GET/PUT）。
  assert.doesNotMatch(source, /code: 'MALFORMED_RESPONSE'/,
    '零活动保存不得伪造「数据格式错误」—— 真正缺的是创建接口')

  // 必须给出可执行的下一步，光说「失败」等于把问题丢回给管理员。
  assert.match(source, /尚未提供「创建活动」接口/, '必须说明缺的是创建接口')
  assert.match(source, /review_rebate_programs/, '必须指出绕过方式落在哪张表')
})

test('零活动提示用 info 而不是 error（这是正常状态，不是故障）', async () => {
  const source = await loadAdminPage()
  // 用 error/warning 会让管理员以为系统坏了；全新部署第一次进来就是零活动。
  const alertBlock = source.slice(source.indexOf('尚未创建评价返现活动') - 400,
    source.indexOf('尚未创建评价返现活动') + 200)
  assert.match(alertBlock, /type="info"/, '「尚未创建」必须用 info 级提示')
})
