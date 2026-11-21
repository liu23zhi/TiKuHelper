// 「设备上限/设备列表显示未知」的根因钉子。
//
// 后端两个接口的响应体都是 **{data: {...}}** 信封：
//   controller/device_user.go:  c.JSON(200, gin.H{"data": view})
// 而传输层 request.js 的 buildResponsePayload 把**整个响应体**放进 payload.data：
//   const payload = { data: response?.data, ... }
// 于是页面里 res.data === {data: view}，真正的字段在 res.data.data。
//
// 原实现直接 normalizeDeviceLimit(res.data) / normalizeDeviceList(res.data)，
// 读的是信封上的 limit / devices —— 全是 undefined，于是：
//   limitKnown=false → 显示「未知」、来源「未知」、输入框禁用
//   devices=[] → 显示「还没有设备」
// 这就是用户看到的三个现象的共同根因。
//
// 关键教训：**这两个现象同源**。只修上限会留下列表的同一个 bug。

import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeDeviceLimit, normalizeDeviceList } from './myDevicesHelpers.mjs';

// 后端真实响应体（带信封）
const LIMIT_ENVELOPE = {
  data: {
    limit: 0,
    source: 'unlimited',
    unlimited: true,
    override: -1,
    levelLimit: 0,
    levelId: 1,
    levelName: '基础会员',
    upperBound: null,
    editable: true,
    hookReady: true,
  },
};

const LIST_ENVELOPE = {
  data: {
    devices: [{ id: 7, deviceId: 'dev-abc', label: 'Chrome', status: 'active' }],
    total: 1,
    active: 1,
    blocked: 0,
    schemaReady: true,
  },
};

test('normalizeDeviceLimit 必须能接受带信封的响应（后端真实形态）', () => {
  const view = normalizeDeviceLimit(LIMIT_ENVELOPE);
  assert.equal(
    view.limitKnown,
    true,
    'limitKnown 必须为 true —— 这是「未知」文案的直接来源',
  );
  assert.equal(view.limit, 0);
  assert.equal(view.unlimited, true);
  assert.equal(view.sourceText, '等级不限');
  assert.equal(view.levelName, '基础会员');
  assert.equal(view.editable, true);
});

test('normalizeDeviceLimit 仍接受已解包的对象（向后兼容）', () => {
  const view = normalizeDeviceLimit(LIMIT_ENVELOPE.data);
  assert.equal(view.limitKnown, true);
  assert.equal(view.unlimited, true);
});

test('normalizeDeviceList 必须能接受带信封的响应（后端真实形态）', () => {
  const view = normalizeDeviceList(LIST_ENVELOPE);
  assert.equal(view.devices.length, 1, '设备列表不能因信封而变空');
  assert.equal(view.total, 1);
  assert.equal(view.active, 1);
});

test('解包不能把「合法的空列表」误判成信封', () => {
  // 已解包的合法空态：devices 是空数组但有 total=0
  const view = normalizeDeviceList({ devices: [], total: 0, active: 0, blocked: 0 })
  assert.deepEqual(view.devices, [])
  assert.equal(view.total, 0)
});

test('解包不能吞掉顶层同名字段（对象同时有 data 与 limit 时以顶层为准）', () => {
  const view = normalizeDeviceLimit({ limit: 3, data: { limit: 9 } })
  assert.equal(view.limit, 3, '顶层 limit 存在时应优先使用，不被 data 覆盖');
});
