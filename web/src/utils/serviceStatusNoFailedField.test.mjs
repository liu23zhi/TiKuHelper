// 服务状态「数据格式暂不受支持」的回归钉子。
//
// 真实事故：后端 2026-11-15 从公开 summary 端点移除了 failedRequests
// （理由：AuthOrAccessToken 是普通用户可访问的，暴露失败数等于泄漏「搜题桶失败 N 次」；
//  TestPublicEndpointsHideFailedRequests 反向断言 JSON 里不得出现该字段）。
//
// 前端 isSupportedServiceStatusSummary 仍把它列为必需 -> HTTP 200 但校验失败 ->
// 页面显示「服务状态数据格式暂不受支持，请稍后重试」。用户看到的是「功能坏了」。
//
// 这组用例钉两件事：① 缺 failedRequests 必须能解析 ② 有它时行为不变（向后兼容）。

import test from 'node:test';
import assert from 'node:assert/strict';

import {
  isSupportedServiceStatusSummary,
  buildServiceStatusView,
} from './serviceStatusSummary.mjs';

// 后端现在真实返回的形状（无 failedRequests）。
const CURRENT_PAYLOAD = {
  schemaVersion: 1,
  status: 'ok',
  successRate: 0.98,
  successRateAvailable: true,
  totalRequests: 100,
  successRequests: 98,
  avgLatencyMs: 120,
  windowMinutes: 5,
  retained: 3,
  capacity: 1440,
};

test('后端当前返回形状（无 failedRequests）必须被接受', () => {
  assert.equal(
    isSupportedServiceStatusSummary(CURRENT_PAYLOAD),
    true,
    '缺 failedRequests 时不应判为「格式不受支持」—— 这正是页面报错的根因',
  );
});

test('缺 failedRequests 时视图仍可用，且明确标记「没有失败指标」', () => {
  const view = buildServiceStatusView(CURRENT_PAYLOAD);
  assert.equal(view.dataState, 'measured');
  assert.equal(view.hasFailedMetric, false, '字段缺失时 hasFailedMetric 必须为 false');
  assert.equal(view.failedText, '--', 'formatCount(null) 保持 -- 语义，供不渲染时忽略');
});

test('带 failedRequests 的旧形状仍被接受（向后兼容）', () => {
  const legacy = { ...CURRENT_PAYLOAD, failedRequests: 2 };
  assert.equal(isSupportedServiceStatusSummary(legacy), true);
  const view = buildServiceStatusView(legacy);
  assert.equal(view.hasFailedMetric, true);
  assert.equal(view.failedText, '2');
});

test('failedRequests 非法值仍要拒绝（不是变成「完全不校验」）', () => {
  assert.equal(
    isSupportedServiceStatusSummary({ ...CURRENT_PAYLOAD, failedRequests: -1 }),
    false,
    '给了负数说明数据确实坏了，必须拒绝',
  );
  assert.equal(
    isSupportedServiceStatusSummary({ ...CURRENT_PAYLOAD, failedRequests: 'abc' }),
    false,
  );
});

test('既有必需字段仍然必需（放宽 failedRequests 不能顺带放过别人）', () => {
  for (const field of ['totalRequests', 'successRequests', 'avgLatencyMs', 'windowMinutes']) {
    const broken = { ...CURRENT_PAYLOAD };
    delete broken[field];
    assert.equal(
      isSupportedServiceStatusSummary(broken),
      false,
      `去掉 ${field} 后必须判为不受支持`,
    );
  }
});
