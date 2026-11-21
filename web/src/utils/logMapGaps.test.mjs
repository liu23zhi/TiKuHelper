// 日志模块/注册来源中文映射的完整性判据。
//
// 存在这个文件的原因：后端 module 取值集合与前端 LOG_MODULE_OPTIONS 是两套
// **各自维护**的字面量，没有任何机制保证同步。实测后端用了 17 个值、前端只映射
// 11 个，于是「操作日志」页会把 account/binding/ai/... 原样显示成英文。
//
// 判据只钉「这些值必须有中文标签」，不钉具体措辞（措辞会改，缺映射不会）。

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { LOG_MODULE_OPTIONS, buildLogModuleLabel, buildLogActionLabel } from './logListHelpers.mjs';
import { buildRegisterSourceLabel } from './userPortalHelpers.mjs';


// 后端生产代码里实际出现过的 module 值（逐个 git grep 核实过来源）。
// 保留变量传入的历史值，并读取生产 Go 代码的直接写入值，新增模块会自动参与回归。
const backendModules = new Set([
  'auth', 'question', 'search', 'ai_fallback', 'captcha', 'user',
  'billing', 'sms', 'recharge', 'access_token', 'export', 'import',
  'account',   // account_log.go:39 accountLogModule + username_release.go:81
  'binding',   // user_bindings.go:38 bindingLogModule
  'ai', 'review_rebate', 'tiku', 'userexam_tool', 'unknown_module', 'ticket',
]);
const backendRoot = fileURLToPath(new URL('../../../internal/', import.meta.url));
for (const directory of ['controller', 'service']) {
  for (const filename of fs.readdirSync(path.join(backendRoot, directory))) {
    if (!filename.endsWith('.go') || filename.endsWith('_test.go')) continue;
    const source = fs.readFileSync(path.join(backendRoot, directory, filename), 'utf8');
    for (const pattern of [/recordAccountAudit\(\s*[^,]+,\s*[^,]+,\s*"([^"]+)"/g, /Module\s*:\s*"([^"]+)"/g]) {
      for (const match of source.matchAll(pattern)) backendModules.add(match[1]);
    }
  }
}
const BACKEND_MODULES = [...backendModules];

test('后端每个 module 值都有中文标签（不能原样显示英文）', () => {
  const missing = BACKEND_MODULES.filter(
    (value) => !LOG_MODULE_OPTIONS.some((option) => option.value === value),
  );
  assert.deepEqual(
    missing,
    [],
    `以下 module 缺中文映射，操作日志页会显示成英文原文：${missing.join(', ')}`,
  );
});

test('buildLogModuleLabel 对每个后端值都返回中文（不回落成原值）', () => {
  for (const value of BACKEND_MODULES) {
    const label = buildLogModuleLabel(value);
    assert.notEqual(label, value, `module=${value} 回落成了英文原文，说明没有映射`);
    assert.match(label, /[\u4e00-\u9fa5A-Z]/, `module=${value} 的标签不是可读文本：${label}`);
  }
});

test('注册来源 import 有中文标签（import_data.go:1172 会写入这个值）', () => {
  assert.equal(buildRegisterSourceLabel('import'), '导入创建');
  // 既有的三种不能被改坏
  assert.equal(buildRegisterSourceLabel('public'), '公开注册');
  assert.equal(buildRegisterSourceLabel('admin'), '后台创建');
  assert.equal(buildRegisterSourceLabel('system'), '系统初始化创建');
});

test('邀请配置与工单账号处理使用对应动作名称，保持验证码动作原语义',()=>{
  assert.equal(buildLogModuleLabel('referral'),'邀请返佣')
  assert.equal(buildLogActionLabel(1,'referral'),'邀请配置修改')
  assert.equal(buildLogActionLabel(20,'ticket'),'工单账号处理')
  assert.equal(buildLogActionLabel(20,'captcha'),'远程生成')
})
