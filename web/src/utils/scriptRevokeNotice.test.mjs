// 「我的脚本」通知区的钉子（2026-11-17）。
//
// 判据全部写字面量，不引用 helper 里的常量 —— 常量漂移时本文件必须变红。
// （本仓踩过「期望值与被测实现共享常量导致断言恒真」这一类假钉子。）

import test from 'node:test';
import assert from 'node:assert/strict';

import {
  normalizeScriptRevokeNotice,
  collectScriptRevokeNotices,
  scriptNoticeAlertType,
} from './scriptRevokeNotice.mjs';

test('① 心跳中断：必须可见且是 warning 级别', () => {
  const view = normalizeScriptRevokeNotice({
    level: 'warning',
    reason: 'heartbeat_timeout',
    message: '答题令牌已撤销：客户端心跳中断。',
  });
  assert.equal(view.visible, true);
  assert.equal(view.level, 'warning');
  assert.equal(view.reason, 'heartbeat_timeout');
  // hint 是前端补充说明，让用户一眼知道是「客户端心跳」问题
  assert.equal(view.hint, '客户端心跳中断');
});

test('② none / 缺失 / 非对象：一律不渲染', () => {
  for (const raw of [undefined, null, {}, { level: 'none' }, { level: '' }, 'oops', 42]) {
    const view = normalizeScriptRevokeNotice(raw);
    assert.equal(view.visible, false, `raw=${JSON.stringify(raw)} 不该渲染`);
    assert.equal(view.level, 'none');
  }
});

test('③ 未知级别降级为 info，未知原因原样保留', () => {
  const view = normalizeScriptRevokeNotice({
    level: 'critical',
    reason: 'some_future_reason',
    message: '后端新文案',
  });
  // 未知级别：不能当 warning 去抢注意力，也不能丢弃 —— 降级为 info
  assert.equal(view.visible, true);
  assert.equal(view.level, 'info');
  // 未知原因必须原样保留，否则运维看不到后端到底写了什么
  assert.equal(view.reason, 'some_future_reason');
  // 已知映射里没有它，hint 必须为空（不编一个说法）
  assert.equal(view.hint, '');
  assert.equal(view.message, '后端新文案');
});

test('④ 后端没给文案时给兜底，不出现空提示条', () => {
  const view = normalizeScriptRevokeNotice({ level: 'info', reason: '' });
  assert.equal(view.visible, true);
  assert.notEqual(view.message.trim(), '');
});

test('⑤ 聚合：warning 排在 info 前面，且带上脚本名', () => {
  const notices = collectScriptRevokeNotices([
    { id: 1, name: '普通脚本', revokeNotice: { level: 'info', reason: 'admin_revoked', message: '管理员撤销' } },
    { id: 2, name: '考试脚本', revokeNotice: { level: 'warning', reason: 'heartbeat_timeout', message: '心跳中断' } },
    { id: 3, name: '没事的脚本' },
  ]);
  assert.equal(notices.length, 2);
  // warning 必须在最前：那是唯一需要用户行动的
  assert.equal(notices[0].scriptId, 2);
  assert.equal(notices[0].level, 'warning');
  assert.equal(notices[0].scriptName, '考试脚本');
  assert.equal(notices[1].scriptId, 1);
});

test('⑥ 聚合：非数组、空数组、脏项都不炸', () => {
  assert.deepEqual(collectScriptRevokeNotices(undefined), []);
  assert.deepEqual(collectScriptRevokeNotices(null), []);
  assert.deepEqual(collectScriptRevokeNotices([]), []);
  assert.deepEqual(collectScriptRevokeNotices([null, undefined, 1, 'x']), []);
});

test('⑦ 脚本没名字时用兜底名，不能显示空白', () => {
  const notices = collectScriptRevokeNotices([
    { id: 7, name: '   ', revokeNotice: { level: 'warning', reason: 'heartbeat_timeout' } },
  ]);
  assert.equal(notices.length, 1);
  assert.equal(notices[0].scriptName, '未命名的脚本');
});

test('⑧ alert 类型映射：只有 warning 用警示色', () => {
  assert.equal(scriptNoticeAlertType('warning'), 'warning');
  assert.equal(scriptNoticeAlertType('info'), 'info');
  assert.equal(scriptNoticeAlertType('none'), 'info');
});
