// 「跳过绑定门槛」按钮的钉子（2026-11-17）。
//
// 判据全部写字面量，不引用 helper 里的常量 —— 常量漂移时本文件必须变红。

import test from 'node:test';
import assert from 'node:assert/strict';

import {
  shouldShowSkipBindingButton,
  buildCreateScriptPayload,
  isBindingsRequiredError,
  buildSkipBindingConfirmText,
} from './scriptSkipBinding.mjs';

test('① 按钮仅管理员可见', () => {
  assert.equal(shouldShowSkipBindingButton({ isAdmin: true }), true);
  // 普通用户看不到 —— 但这只是可用性，真正拦人的是后端
  assert.equal(shouldShowSkipBindingButton({ isAdmin: false }), false);
});

test('② 上下文缺失/畸形时不显示（默认关闭，不是默认打开）', () => {
  for (const ctx of [undefined, null, {}, { isAdmin: 'true' }, { isAdmin: 1 }, 'admin']) {
    assert.equal(shouldShowSkipBindingButton(ctx), false, `ctx=${JSON.stringify(ctx)} 不得显示按钮`);
  }
});

test('③ 勾选跳过后 payload 带 skipBindingCheck: true', () => {
  const payload = buildCreateScriptPayload({ name: 'a', versionId: 3 }, true);
  assert.equal(payload.skipBindingCheck, true);
  assert.equal(payload.name, 'a');
  assert.equal(payload.versionId, 3);
});

test('④ 未勾选时 payload 不出现该字段（不是 false，是不出现）', () => {
  const payload = buildCreateScriptPayload({ name: 'a' }, false);
  assert.equal(Object.hasOwn(payload, 'skipBindingCheck'), false);
});

test('⑤ 上一次勾过的脏值必须被清掉', () => {
  // 场景：用户先勾了跳过、创建失败后取消勾选重试。
  // 若复用同一个 payload 对象而不清理，会继续以 true 提交 —— 静默绕过门槛。
  const payload = buildCreateScriptPayload({ name: 'a', skipBindingCheck: true }, false);
  assert.equal(Object.hasOwn(payload, 'skipBindingCheck'), false);
});

test('⑥ 不修改入参（纯函数）', () => {
  const original = { name: 'a', skipBindingCheck: true };
  buildCreateScriptPayload(original, false);
  assert.equal(original.skipBindingCheck, true, 'helper 不得就地改写调用方对象');
});

test('⑦ 非布尔真值不当作跳过', () => {
  for (const skip of ['true', 1, {}, []]) {
    const payload = buildCreateScriptPayload({ name: 'a' }, skip);
    // 后端只认真正的布尔 true；传字符串 "true" 是前端 bug，
    // 与其让它悄悄生效，不如不写这个字段（后端会照常拦）
    assert.equal(Object.hasOwn(payload, 'skipBindingCheck'), false, `skip=${JSON.stringify(skip)}`);
  }
});

test('⑧ 缺绑定错误：识别 code', () => {
  assert.equal(isBindingsRequiredError({ code: 'SCRIPT_BINDINGS_REQUIRED' }), true);
  assert.equal(isBindingsRequiredError({ response: { data: { code: 'SCRIPT_BINDINGS_REQUIRED' } } }), true);
});

test('⑨ 缺绑定错误：code 丢失时退回文案匹配', () => {
  assert.equal(isBindingsRequiredError({ message: '创建脚本前请绑定手机号和微信' }), true);
});

test('⑩ 其它错误不得误判为缺绑定', () => {
  for (const err of [
    undefined,
    null,
    {},
    { code: 'SCRIPT_FORBIDDEN' },
    { message: '创建脚本失败' },
    'oops',
  ]) {
    assert.equal(isBindingsRequiredError(err), false, `err=${JSON.stringify(err)} 不该判成缺绑定`);
  }
});

test('⑪ 二次确认文案：管理员与非管理员不同', () => {
  const admin = buildSkipBindingConfirmText({ isAdmin: true });
  assert.notEqual(admin.title.trim(), '');
  // 必须说清后果（绕过的是合规门槛），否则用户不知道自己在做什么
  assert.match(admin.content, /绑定/);
  assert.notEqual(admin.okText.trim(), '');

  const user = buildSkipBindingConfirmText({ isAdmin: false });
  assert.match(user.content, /管理员/);
});
