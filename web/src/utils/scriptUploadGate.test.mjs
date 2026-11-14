import test from 'node:test'
import assert from 'node:assert/strict'
import {
  describeScriptAdminAction,
  describeScriptUploadGate,
  validateScriptUploadFile,
  validateScriptVersionLabel,
} from './scriptUploadGate.mjs'

// 管理员上传 EXE 的提交判据（诉求 ① / ③）。
//
// 用户原话：「上传按钮无效。需要勾选『确认该原生 C 版本已验证新版租约』并填写版本号
// 才能正常使用，请添加提示。**请去除**『确认该原生 C 版本已验证新版租约』。」
//
// 这里的判据：
//   1. 勾选框已去掉 ⇒ 只剩版本号一个必填项，按钮 gate 必须只依赖它；
//   2. 缺版本号时 disabled，且 message **说清缺什么**；
//   3. 管理员的动作语义是「上传新版本 EXE」，不是「新建脚本」。

test('去掉勾选后：只填版本号即可提交（不再有任何租约勾选前置）', () => {
  const gate = describeScriptUploadGate({ version: '1.0.0' })
  assert.equal(gate.disabled, false, '版本号齐全时必须可提交')
  assert.equal(gate.message, '')
  // 旧实现要求 nativeLeaseConfirmed=true 才 enabled；现在多传这个字段也不该影响判据。
  const withLegacyFlag = describeScriptUploadGate({ version: '1.0.0', nativeLeaseConfirmed: false })
  assert.equal(withLegacyFlag.disabled, false, '租约勾选不得再参与 gate')
})

test('缺版本号时按钮 disabled，且文案指出缺的是版本号', () => {
  for (const version of ['', '   ', undefined, null]) {
    const gate = describeScriptUploadGate({ version })
    assert.equal(gate.disabled, true, `version=${String(version)} 必须 disabled`)
    // ⚠️ 变异判据：把这条 gate 删掉（恒 return disabled:false）会让本用例红。
    assert.match(gate.message, /版本号/, `文案必须说清缺版本号，实际：${gate.message}`)
  }
})

test('版本号格式校验给出具体原因，不是「请填写完整」', () => {
  assert.match(validateScriptVersionLabel('a'.repeat(65)), /64/)
  assert.match(validateScriptVersionLabel('1/0'), /斜杠/)
  assert.match(validateScriptVersionLabel('1\\0'), /斜杠/)
  assert.equal(validateScriptVersionLabel('1.0.0'), '')
  assert.equal(validateScriptVersionLabel('  v2  '), '')
})

test('上传中不可重复提交，且文案说明正在上传', () => {
  const gate = describeScriptUploadGate({ version: '1.0.0', uploading: true })
  assert.equal(gate.disabled, true)
  assert.match(gate.message, /正在上传/)
})

test('选中的文件不是 .exe 时拦下并说明原因', () => {
  assert.match(validateScriptUploadFile({ name: 'a.zip' }), /\.exe/)
  assert.equal(validateScriptUploadFile({ name: 'A.EXE' }), '')
  const gate = describeScriptUploadGate({ version: '1.0.0', file: { name: 'a.zip' } })
  assert.equal(gate.disabled, true)
  assert.match(gate.message, /\.exe/)
})

test('诉求③：管理员动作语义是「上传 EXE 新版本」，不是「新建脚本」', () => {
  const action = describeScriptAdminAction()
  assert.equal(action.primaryLabel, '上传 EXE')
  assert.equal(action.target, 'script-versions/upload')
  // 提示必须明确「上传不会创建脚本」，否则管理员会以为上传完就多了一条记录。
  assert.match(action.hint, /不会创建脚本/)
})
