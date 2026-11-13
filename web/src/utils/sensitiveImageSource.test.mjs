import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import * as helpersEsm from '../pages/SensitiveImage/sensitiveImageHelpers.mjs';

// 敏感图片检测管理页（T-14）。
//
// 本文件**不依赖 vue / axios / node_modules**：web/node_modules 在本机是空壳
// （0 个顶层目录），任何 import 'vue' 的测试都跑不起来。所以这里做两件事：
//   1. 直接 import 页面的纯函数层 sensitiveImageHelpers.mjs 做**行为断言**；
//   2. 用 readFileSync 读 index.vue / SensitiveImageApi.js 做**接线断言**
//      （页面确实把这些纯函数接上了，而不是写完没调用）。
// 行为断言是主判据，接线断言只保证「纯函数没被绕过」。
//
// 四条判据对应关系：
//   ① 两个 Tab 各自能翻页        → buildEntryQuery + 两套分页对象 + onCurrentTableChange
//   ② 手动触发响应写清涉及多少行  → projectDetectResponse + describeDetectOutcome
//   ③ 响应绝不回图片字节与上游原文 → SENSITIVE_IMAGE_FORBIDDEN_FIELDS + 投影白名单
//   ④ 前端契约：两处 tab key + 播放控制器 → SENSITIVE_IMAGE_TABS + createGifPlaybackController

const here = path.dirname(fileURLToPath(import.meta.url));
const pageDir = path.resolve(here, '../pages/SensitiveImage');
const pageSource = readFileSync(path.join(pageDir, 'index.vue'), 'utf8');
const apiSource = readFileSync(path.resolve(here, '../api/SensitiveImageApi.js'), 'utf8');

const {
  SENSITIVE_IMAGE_TAB_QUOTA,
  SENSITIVE_IMAGE_TAB_RECORDS,
  SENSITIVE_IMAGE_TABS,
  SENSITIVE_IMAGE_QUOTA_TAB_STATUS,
  SENSITIVE_IMAGE_QUOTA_TAB_STATUS_OPTIONS,
  SENSITIVE_IMAGE_DETECT_RESULT_FIELDS,
  SENSITIVE_IMAGE_FORBIDDEN_FIELDS,
  readPositiveInt,
  normalizeSha256,
  buildEntryQuery,
  buildDetectPayload,
  projectDetectResponse,
  describeDetectOutcome,
  buildGifControllerOptions,
  createGifPlaybackController,
  shouldOfferGifControls,
  summarizeQuota,
} = helpersEsm;

const SHA_A = 'a'.repeat(64);
const SHA_B = 'b'.repeat(64);

// ───────────────────────────────────────────────────────────────────
// 判据④之外的底线：两个 Tab 的 key 必须是两个**不同**的稳定值。
// 写成同一件事（比如都叫 'records'）会让 UI 上两个标签页共用一份数据源。
// ───────────────────────────────────────────────────────────────────
test('两个 Tab key 互不相同且被唯一定义源暴露', () => {
  assert.equal(SENSITIVE_IMAGE_TAB_QUOTA, 'quota');
  assert.equal(SENSITIVE_IMAGE_TAB_RECORDS, 'records');
  assert.notEqual(SENSITIVE_IMAGE_TAB_QUOTA, SENSITIVE_IMAGE_TAB_RECORDS);
  assert.equal(SENSITIVE_IMAGE_TABS.length, 2, '必须正好两个 Tab');
  const keys = SENSITIVE_IMAGE_TABS.map(tab => tab.key);
  assert.deepEqual(keys, [SENSITIVE_IMAGE_TAB_QUOTA, SENSITIVE_IMAGE_TAB_RECORDS]);
  // 每个 Tab 都必须有非空 label（否则 UI 上是两个空白标签页）。
  for (const tab of SENSITIVE_IMAGE_TABS) {
    assert.ok(String(tab.label || '').trim().length > 0, `${tab.key} 缺少 label`);
    assert.ok(String(tab.description || '').trim().length > 0, `${tab.key} 缺少说明`);
  }
});

// ───────────────────────────────────────────────────────────────────
// 判据① 两个 Tab 各自能翻页
// ───────────────────────────────────────────────────────────────────
test('判据①：两个 Tab 生成各自的查询参数，页码与筛选互不串台', () => {
  // 「超额未检测」Tab 带 status，且默认是 error。
  const quotaQuery = buildEntryQuery(SENSITIVE_IMAGE_TAB_QUOTA, { page: 3, pageSize: 50 });
  assert.equal(quotaQuery.page, 3);
  assert.equal(quotaQuery.pageSize, 50);
  assert.equal(quotaQuery.status, 'error');
  assert.equal(SENSITIVE_IMAGE_QUOTA_TAB_STATUS, 'error');

  // 「全部检测记录」Tab **不带 status**：带空串会让后端语义模糊，
  // 而带任何具体值就不再是「全部」。这里断言的是「没有 status 这个键」。
  const recordQuery = buildEntryQuery(SENSITIVE_IMAGE_TAB_RECORDS, { page: 7, pageSize: 10 });
  assert.equal(recordQuery.page, 7);
  assert.equal(recordQuery.pageSize, 10);
  assert.equal(Object.hasOwn(recordQuery, 'status'), false, '全部检测记录 Tab 不得带 status');

  // 两个 Tab 的页码是独立输入：同一次调用不会把 A 的页码带入 B。
  assert.notEqual(quotaQuery.page, recordQuery.page);
});

test('判据①：页码与每页条数被收敛成合法值（非法输入不得打出无意义请求）', () => {
  // 非法/缺失一律回到 1 / 20，绝不出现 NaN、0、负数。
  for (const bad of [undefined, null, '', 'abc', 0, -5, 1.5, NaN, Infinity]) {
    const query = buildEntryQuery(SENSITIVE_IMAGE_TAB_RECORDS, { page: bad, pageSize: bad });
    assert.equal(query.page, 1, `page=${String(bad)} 必须收敛成 1`);
    assert.equal(query.pageSize, 20, `pageSize=${String(bad)} 必须收敛成 20`);
  }
  // pageSize 上限与后端一致（controller 里 >100 会被打回 20）。
  assert.equal(buildEntryQuery(SENSITIVE_IMAGE_TAB_RECORDS, { pageSize: 1000 }).pageSize, 100);
  // 字符串数字要能正常工作（输入框天然给字符串）。
  assert.equal(buildEntryQuery(SENSITIVE_IMAGE_TAB_RECORDS, { page: '4', pageSize: '30' }).page, 4);
  assert.equal(buildEntryQuery(SENSITIVE_IMAGE_TAB_RECORDS, { page: '4', pageSize: '30' }).pageSize, 30);
});

test('判据①：按 SHA256 精确查时只在标识合法时带上该参数', () => {
  assert.equal(buildEntryQuery(SENSITIVE_IMAGE_TAB_RECORDS, { imageSha256: SHA_A.toUpperCase() }).imageSha256, SHA_A);
  // 非法标识不能被打到网络上（后端会 400，前端先挡）。
  for (const bad of ['', '   ', 'abc', 'z'.repeat(64), SHA_A.slice(0, 63), SHA_A + 'a']) {
    assert.equal(
      Object.hasOwn(buildEntryQuery(SENSITIVE_IMAGE_TAB_RECORDS, { imageSha256: bad }), 'imageSha256'),
      false,
      `非法标识 "${bad}" 不得进入请求参数`,
    );
  }
});

test('判据①：页面持有两套独立分页对象，且翻页只改当前 Tab', () => {
  // 接线断言：两套分页对象必须都存在于页面里（缺一套就意味着两个 Tab 共用页码）。
  assert.match(pageSource, /const\s+quotaPagination\s*=\s*reactive\(/, '缺少超额 Tab 的分页对象');
  assert.match(pageSource, /const\s+recordPagination\s*=\s*reactive\(/, '缺少全部记录 Tab 的分页对象');
  assert.match(pageSource, /quotaPagination\s*\.\s*current/, '超额 Tab 分页对象未被使用');
  assert.match(pageSource, /recordPagination\s*\.\s*current/, '全部记录 Tab 分页对象未被使用');
  // onCurrentTableChange 必须按 activeTab 选目标对象（写死一个就是串页）。
  assert.match(
    pageSource,
    /const\s+target\s*=\s*activeTab\.value\s*===\s*QUOTA_TAB\s*\?\s*quotaPagination\s*:\s*recordPagination/,
    'onCurrentTableChange 必须按当前 Tab 选择分页对象',
  );
  // 两个请求函数必须各自存在（一个函数管两个 Tab 会共用 loading / error）。
  assert.match(pageSource, /const\s+loadQuotaRows\s*=\s*async/, '缺少超额 Tab 的读取函数');
  assert.match(pageSource, /const\s+loadRecordRows\s*=\s*async/, '缺少全部记录 Tab 的读取函数');
  // 页码切页时 pageSize 变化必须回到第 1 页（否则会停在越界页）。
  assert.match(pageSource, /target\.current\s*=\s*nextSize\s*===\s*target\.pageSize\s*\?\s*Number\(page\?\.current\)\s*\|\|\s*1\s*:\s*1/);
});

// ───────────────────────────────────────────────────────────────────
// 判据② 手动触发后响应里必须写清本次涉及多少行
// ───────────────────────────────────────────────────────────────────
test('判据②：按 SHA256 触发时把 affectedAttachments 提成显式数字并写进结论', () => {
  // 关键场景：同一 SHA256 被 4 张工单引用（4 行附件）。
  const view = projectDetectResponse({
    result: { imageSha256: SHA_A, state: 'clean', cacheHit: false, upstreamCalled: true, scanState: 'approved' },
    affectedAttachments: 4,
  });
  assert.equal(view.mode, 'image');
  assert.equal(view.affectedAttachments, 4);
  const text = describeDetectOutcome(view);
  assert.match(text, /4\s*行/, `结论必须报出涉及行数，实际：${text}`);
  assert.match(text, /clean/);
  assert.match(text, /SHA256/, '结论必须说明「按 SHA256 生效」，否则管理员会以为只改了一个工单');
});

test('判据②：affectedAttachments 缺字段时写「未知」，绝不退化成 0', () => {
  // 这是判据②最容易做假的地方：把缺失当 0，界面会显示「更新了 0 行」，
  // 管理员会以为什么都没改（实际是后端没回这个字段）。
  const missing = projectDetectResponse({ result: { imageSha256: SHA_A, state: 'sensitive' } });
  assert.equal(missing.affectedAttachments, null, '缺字段必须是 null 而不是 0');
  const text = describeDetectOutcome(missing);
  assert.match(text, /未知/, `缺字段时必须写「未知」，实际：${text}`);
  assert.doesNotMatch(text, /0\s*行/, '缺字段时不得出现「0 行」');
});

test('判据②：真实的 0 行仍然显示 0（不能把 0 也当成未知）', () => {
  const view = projectDetectResponse({ result: { state: 'clean' }, affectedAttachments: 0 });
  assert.equal(view.affectedAttachments, 0);
  const text = describeDetectOutcome(view);
  assert.match(text, /0\s*行/, `真实的 0 行必须显示 0，实际：${text}`);
  assert.doesNotMatch(text, /未知/);
});

test('判据②：按工单批量触发时报出 total / detected / skipped 三个数', () => {
  const view = projectDetectResponse({
    ticketId: 77,
    total: 9,
    detected: 7,
    skipped: 2,
    results: [{ state: 'clean' }, { state: 'sensitive' }],
  });
  assert.equal(view.mode, 'ticket');
  assert.equal(view.ticketId, 77);
  assert.equal(view.total, 9);
  assert.equal(view.detected, 7);
  assert.equal(view.skipped, 2);
  assert.equal(view.resultCount, 2);
  const text = describeDetectOutcome(view);
  assert.match(text, /共\s*9\s*张/);
  assert.match(text, /重新检测\s*7\s*张/);
  assert.match(text, /跳过\s*2\s*张/);
});

test('判据②：同步失败信息必须透出（缓存已改但附件没跟上，展示会不一致）', () => {
  const view = projectDetectResponse({
    result: { state: 'clean' },
    affectedAttachments: 3,
    attachSyncError: 'database is locked',
  });
  assert.equal(view.attachSyncError, 'database is locked');
  // 页面必须把它渲染出来，不能只在 console 里丢掉。
  assert.match(pageSource, /detectOutcome\.attachSyncError/, '页面必须展示 attachSyncError');
});

test('判据②：两种寻址方式必须显式分流，同时给时告知忽略了工单号', () => {
  const shaOnly = buildDetectPayload({ imageSha256: SHA_A });
  assert.equal(shaOnly.error, '');
  assert.equal(shaOnly.payload.imageSha256, SHA_A);
  assert.equal(shaOnly.payload.ticketId, 0);
  assert.equal(shaOnly.ignoredTicketId, false);

  const ticketOnly = buildDetectPayload({ ticketId: '42' });
  assert.equal(ticketOnly.error, '');
  assert.equal(ticketOnly.payload.ticketId, 42);
  assert.equal(ticketOnly.payload.imageSha256, '');
  assert.equal(ticketOnly.ignoredTicketId, false);

  // 两者都给：只处理 SHA256，且**必须回报忽略了工单号**（静默二选一 = 谎报批量跑了）。
  const both = buildDetectPayload({ imageSha256: SHA_A, ticketId: '42' });
  assert.equal(both.error, '');
  assert.equal(both.payload.imageSha256, SHA_A);
  assert.equal(both.ignoredTicketId, true);
  assert.match(
    pageSource,
    /built\.ignoredTicketId/,
    '页面必须在同时填写时提示工单号被忽略',
  );

  // 都不给：明确报错，不发请求。
  const none = buildDetectPayload({});
  assert.equal(none.payload, null);
  assert.match(none.error, /64\s*位|工单编号/);
});

// ───────────────────────────────────────────────────────────────────
// 判据③ 响应绝不回图片字节与上游原文
// ───────────────────────────────────────────────────────────────────
test('判据③：投影结果只保留白名单字段，字节与上游原文一律丢弃', () => {
  // 模拟一个「将来有人为方便加了 preview」的后端响应。
  const hostile = {
    result: {
      imageSha256: SHA_A,
      state: 'sensitive',
      reason: '上游判定为敏感图片',
      // ↓ 以下全部是禁止项
      ciphertext: 'BASE64CIPHERTEXT==',
      nonce: 'NONCEBYTES',
      plain: 'RAWBINARY',
      bytes: [1, 2, 3],
      imageBase64: 'iVBORw0KGgo=',
      preview: 'data:image/png;base64,iVBORw0KGgo=',
      thumbnail: 'thumb-data',
      originalText: '上游原始响应体',
      rawResponse: '{"code":0,"data":{"nsfw":true}}',
    },
    affectedAttachments: 2,
  };
  const view = projectDetectResponse(hostile);

  // 白名单内的字段保留。
  assert.equal(view.result.imageSha256, SHA_A);
  assert.equal(view.result.state, 'sensitive');
  assert.equal(view.result.reason, '上游判定为敏感图片');

  // 禁止项一个都不能出现 —— 逐个断言，报错时能指名道姓。
  for (const field of SENSITIVE_IMAGE_FORBIDDEN_FIELDS) {
    assert.equal(
      Object.hasOwn(view.result, field),
      false,
      `判据③：投影结果里出现了禁止字段 "${field}"（图片字节或上游原文）`,
    );
  }
  // 整体序列化后也不能残留这些值的痕迹。
  const serialized = JSON.stringify(view);
  for (const marker of ['BASE64CIPHERTEXT==', 'NONCEBYTES', 'RAWBINARY', 'iVBORw0KGgo=', 'thumb-data', '上游原始响应体']) {
    assert.equal(serialized.includes(marker), false, `判据③：序列化结果残留了 ${marker}`);
  }
});

test('判据③：工单批量路径的 results[] 逐项也被投影（不能只净化第一层）', () => {
  const view = projectDetectResponse({
    ticketId: 5,
    total: 2,
    detected: 2,
    skipped: 0,
    results: [
      { state: 'clean', imageSha256: SHA_A, ciphertext: 'LEAK-1', originalText: '上游原文 1' },
      { state: 'sensitive', imageSha256: SHA_B, bytes: [9, 9], preview: 'LEAK-2' },
    ],
  });
  assert.equal(view.results.length, 2);
  const serialized = JSON.stringify(view);
  for (const leak of ['LEAK-1', 'LEAK-2', '上游原文 1']) {
    assert.equal(serialized.includes(leak), false, `判据③：results[] 里泄漏了 ${leak}`);
  }
  for (const item of view.results) {
    for (const field of SENSITIVE_IMAGE_FORBIDDEN_FIELDS) {
      assert.equal(Object.hasOwn(item, field), false, `判据③：results[] 单项含禁止字段 "${field}"`);
    }
  }
});

test('判据③：白名单本身不含任何字节/原文类字段（防「把 preview 加进白名单」）', () => {
  const lowered = SENSITIVE_IMAGE_DETECT_RESULT_FIELDS.map(field => field.toLowerCase());
  for (const field of SENSITIVE_IMAGE_FORBIDDEN_FIELDS) {
    assert.equal(
      lowered.includes(field.toLowerCase()),
      false,
      `判据③：禁止字段 "${field}" 出现在允许白名单里`,
    );
  }
  // 白名单必须仍然覆盖页面真正需要的结论字段（否则「净化」会净化掉有用信息）。
  for (const needed of ['imageSha256', 'state', 'reason', 'scanState', 'cacheHit', 'upstreamCalled']) {
    assert.ok(lowered.includes(needed.toLowerCase()), `白名单缺少必要字段 ${needed}`);
  }
});

test('判据③：API 层不含任何返回图片字节的端点（页面只走检测/额度接口）', () => {
  // 页面若需要预览字节，必须走独立的附件接口；检测管理接口不提供。
  // 这条断言挡住「顺手在 SensitiveImageApi 里加一个 getImageBytes」。
  assert.doesNotMatch(apiSource, /responseType\s*:\s*['"]blob['"]/i, '判据③：SensitiveImageApi 不得声明 blob 响应类型');
  assert.doesNotMatch(apiSource, /\/image\b|image\?|attachment.*image|preview/i, '判据③：SensitiveImageApi 不得包含取图端点');
  // 检测响应也必须在页面上不做任何 img src 拼接。
  assert.doesNotMatch(
    pageSource,
    /:src=/,
    '判据③：页面不得渲染检测记录的图片内容（无字节可渲染，出现 :src 说明在拼数据）',
  );
});

// ───────────────────────────────────────────────────────────────────
// 判据④ 前端契约：两处 tab key + 播放控制器
// ───────────────────────────────────────────────────────────────────
test('判据④：页面源码引用两个 tab key（不是写字面量导致与 helper 漂移）', () => {
  // 唯一来源必须是 helper 导出的常量。
  assert.match(pageSource, /SENSITIVE_IMAGE_TAB_QUOTA/, '页面未引用超额 Tab 的 key 常量');
  assert.match(pageSource, /SENSITIVE_IMAGE_TAB_RECORDS/, '页面未引用全部记录 Tab 的 key 常量');
  assert.match(pageSource, /SENSITIVE_IMAGE_TABS/, '页面未引用 Tab 定义数组');
  // v-for 渲染两个 Tab（写死两个 a-tab-pane 就无法从常量派生）。
  assert.match(pageSource, /v-for="tab in tabs"/, '页面必须按 tabs 数组渲染 Tab');
  // a-tabs 绑定了受控的 activeKey（不受控的话切 Tab 状态不可读）。
  assert.match(pageSource, /v-model:activeKey="activeTab"/, 'Tab 必须受控绑定 activeTab');
});

test('判据④：GIF 播放控制器是真实状态机（播放/暂停/重播/逐帧/取模回绕）', () => {
  const ctrl = createGifPlaybackController({ frameCount: 4, frameDurationMs: 100 });
  assert.equal(ctrl.playing, true, '默认应播放');
  assert.equal(ctrl.frameIndex, 0);
  assert.equal(ctrl.frameLabel, '1 / 4');

  // tick 推进：100ms 恰好一帧。
  assert.equal(ctrl.tick(100), 1);
  assert.equal(ctrl.frameLabel, '2 / 4');
  // 一次大 delta 跨多帧。
  assert.equal(ctrl.tick(250), 3, '250ms 应从第 1 帧推进到第 3 帧');
  // 取模回绕（不能越界）。
  assert.equal(ctrl.tick(100), 0, '越过末帧必须回绕到 0');
  assert.equal(ctrl.frameLabel, '1 / 4');

  // 暂停：tick 不推进帧，且不累积时间（恢复后不会跳帧）。
  ctrl.pause();
  assert.equal(ctrl.playing, false);
  assert.equal(ctrl.tick(5000), 0, '暂停期间不得推进帧');
  assert.equal(ctrl.elapsedMs, 0, '暂停期间不得累积时间（否则恢复瞬间会跳很多帧）');
  ctrl.play();
  assert.equal(ctrl.tick(100), 1, '恢复后从当前帧继续推进');

  // 暂停后再播放必须接着看，而不是跳回第一帧。
  ctrl.selectFrame(2);
  ctrl.pause();
  ctrl.play();
  assert.equal(ctrl.frameIndex, 2, '暂停/播放不得重置帧序号');

  // 重播：帧序号与累计时长一起归零，并回到播放态。
  ctrl.pause();
  ctrl.tick(50);
  ctrl.restart();
  assert.equal(ctrl.frameIndex, 0);
  assert.equal(ctrl.elapsedMs, 0);
  assert.equal(ctrl.playing, true, '重播必须回到播放态');

  // toggle 语义。
  assert.equal(ctrl.toggle(), false);
  assert.equal(ctrl.toggle(), true);

  // selectFrame 越界要回绕而不是报错/越界。
  assert.equal(ctrl.selectFrame(-1), 3, '负索引必须回绕到末帧');
  assert.equal(ctrl.selectFrame(9), 1, '越界索引必须取模回绕');
  assert.equal(ctrl.selectFrame('abc'), 0, '非法索引必须收敛到 0');
});

test('判据④：播放控制器对非法构造参数有确定行为（不产生 NaN 帧）', () => {
  for (const bad of [0, -3, 'abc', null, undefined]) {
    const ctrl = createGifPlaybackController({ frameCount: bad, frameDurationMs: bad });
    assert.equal(ctrl.frameCount >= 1, true, `frameCount=${String(bad)} 必须是正整数`);
    assert.equal(ctrl.frameDurationMs >= 1, true, `frameDurationMs=${String(bad)} 必须是正整数`);
    const index = ctrl.tick(999999);
    assert.equal(Number.isInteger(index), true, 'tick 必须返回整数帧号');
    assert.ok(index >= 0 && index < ctrl.frameCount, 'tick 返回值必须在 [0, frameCount) 内');
  }
});

test('判据④：页面确实接上了播放控制器与其四个操作', () => {
  assert.match(pageSource, /createGifPlaybackController/, '页面未使用播放控制器');
  for (const action of ['toggleGifPlayback', 'restartGifPlayback', 'stepGifFrame', 'openGifPreview', 'closeGifPreview']) {
    assert.match(pageSource, new RegExp(action), `页面缺少播放控制操作 ${action}`);
  }
  // 控制器实例必须在卸载时停掉定时器（不然切页后还在跑）。
  assert.match(pageSource, /onBeforeUnmount\(\(\)\s*=>\s*\{[\s\S]*stopGifTimer/, '卸载时必须停止播放定时器');
  // 只有动图才给控制条。
  assert.match(pageSource, /shouldOfferGifControls/, '页面未按 contentType 判断是否给播放控制条');
});

test('判据④：只有动图类型才产生播放控制（静态图不得出现播放按钮）', () => {
  assert.equal(shouldOfferGifControls('image/gif'), true);
  assert.equal(shouldOfferGifControls('IMAGE/GIF'), true);
  assert.equal(shouldOfferGifControls(' image/webp '), true);
  assert.equal(shouldOfferGifControls('image/apng'), true);
  // 静态图与缺失类型都不给控制条。
  for (const type of ['image/png', 'image/jpeg', '', null, undefined, 'application/pdf']) {
    assert.equal(shouldOfferGifControls(type), false, `${String(type)} 不该有播放控制`);
  }
});

// ───────────────────────────────────────────────────────────────────
// 额度：与「工单图片张数」是两件事
// ───────────────────────────────────────────────────────────────────
test('额度读数：remaining 缺失时不自己算（不能被拼成负数）', () => {
  // dailyUserLimit=0（未配上限）时后端不加 remaining 字段。
  const noLimit = summarizeQuota({ enabled: true, perTicketLimit: 50, dailyUserLimit: 0 });
  assert.equal(noLimit.remaining, null, 'remaining 缺失必须是 null，不能算成 0-0 或负数');
  assert.equal(noLimit.dailyUserLimit, 0);

  const full = summarizeQuota({
    enabled: true, perTicketLimit: 50, dailyUserLimit: 20,
    userId: 9301, date: '2026-11-13', usedCount: 17, remaining: 3,
  });
  assert.equal(full.usedCount, 17);
  assert.equal(full.remaining, 3);
  assert.equal(full.date, '2026-11-13');
  assert.equal(full.userId, 9301);

  // enabled 只认真正的 true（'true' 字符串不算）。
  assert.equal(summarizeQuota({ enabled: 'true' }).enabled, false);
  assert.equal(summarizeQuota({ enabled: true }).enabled, true);
});

test('额度配置：只提交改动过的键（指针语义，全提交会顺带覆盖另外两个）', () => {
  // 页面必须比较「草稿 vs 当前值」再决定提交哪些键。
  assert.match(pageSource, /quotaDraft\.enabled\s*!==\s*\(quota\.value\.enabled\s*===\s*true\)/, '未比较 enabled 是否改动');
  assert.match(pageSource, /perTicket\s*!==\s*quota\.value\.perTicketLimit/, '未比较每工单上限是否改动');
  assert.match(pageSource, /dailyLimit\s*!==\s*quota\.value\.dailyUserLimit/, '未比较每日额度是否改动');
  // 没有改动时要给提示而不是发一个空请求。
  assert.match(pageSource, /没有需要保存的改动/, '无改动时必须提示而不是发空请求');
  // 「50 张/天」是检测额度，不是工单图片张数 —— 页面文案必须区分两件事。
  assert.match(pageSource, /防反复外呼|检测额度/, '页面必须说明检测额度的用途');
  assert.match(pageSource, /每工单上限/, '页面必须区分每工单上限');
});

test('额度 Tab 的局限必须写在页面上（后端没有反查额度耗尽的通道）', () => {
  // 事实：额度耗尽时后端直接 return，缓存表里没有这些图，controller 里
  // 承诺的 quotaExceeded 分支并不存在。页面不得假装列得出来。
  assert.match(pageSource, /从未写入缓存/, '页面必须说明额度耗尽的图不在缓存里');
  assert.match(pageSource, /quota-tab-limitation/, '缺少该说明的锚点');
  // 也不得声称这一 Tab 就是「额度耗尽列表」。
  assert.doesNotMatch(pageSource, /超额未检测的图片列表|额度耗尽的图片列表/, '不得把 error 记录说成额度耗尽列表');
});

test('SHA256 归一化：只接受 64 位十六进制', () => {
  assert.equal(normalizeSha256(SHA_A), SHA_A);
  assert.equal(normalizeSha256(SHA_A.toUpperCase()), SHA_A);
  assert.equal(normalizeSha256(`  ${SHA_B}  `), SHA_B);
  for (const bad of ['', '   ', 'abc', SHA_A.slice(0, 63), SHA_A + 'x', 'g'.repeat(64), null, undefined, 42]) {
    assert.equal(normalizeSha256(bad), '', `非法值 ${String(bad)} 必须归一成空串`);
  }
});

test('readPositiveInt：非法输入一律 null（不返回 NaN/0 冒充有效）', () => {
  assert.equal(readPositiveInt('42'), 42);
  assert.equal(readPositiveInt(42), 42);
  assert.equal(readPositiveInt(' 7 '), 7);
  for (const bad of [0, -1, 1.5, '', '  ', 'abc', null, undefined, NaN, Infinity, {}]) {
    assert.equal(readPositiveInt(bad), null, `${String(bad)} 必须归 null`);
  }
});

test('Tab 状态选项覆盖到 error 与 pending 两种未终结状态', () => {
  const values = SENSITIVE_IMAGE_QUOTA_TAB_STATUS_OPTIONS.map(option => option.value).sort();
  assert.deepEqual(values, ['error', 'pending']);
  assert.equal(SENSITIVE_IMAGE_QUOTA_TAB_STATUS, 'error', '默认必须是 error（最需要人工复核的一类）');
});

test('判据④：帧数未知时必须如实说明，且帧数必须来自记录而非写死常量', () => {
  // ⚠️ 这条**不用源码字符串断言**。上一版就是栽在这里：
  // 断言 `readPositiveInt(record?.frameCount)` 出现在源码里，
  // 而把整行换成 `const frameCount = 1` 时那串字符仍留在别处 → 变异后依旧全绿。
  // 正确做法是调纯函数，喂不同入参看返回值。

  // 记录里带帧数：必须原样读出来（写死 1 的实现会在这里失败）。
  const withFrames = buildGifControllerOptions({ frameCount: 12, frameDurationMs: 80 });
  assert.equal(withFrames.frameCount, 12, '必须从记录读取帧数');
  assert.equal(withFrames.frameDurationMs, 80, '必须从记录读取帧时长');
  assert.equal(withFrames.frameCountKnown, true);
  assert.equal(withFrames.playing, true, '打开时默认播放');

  // 后端当前不下发帧信息：缺省 1 帧 / 100ms，且必须标记为「帧数未知」。
  for (const missing of [{}, null, undefined, { frameCount: 0 }, { frameCount: 'abc' }]) {
    const options = buildGifControllerOptions(missing);
    assert.equal(options.frameCount, 1, '缺省必须是 1 帧');
    assert.equal(options.frameDurationMs, 100, '缺省必须是 100ms');
    assert.equal(options.frameCountKnown, false, '缺省必须标记为「帧数未知」');
  }

  // 单帧记录也算「帧数未知」：1 帧既可能是真的单帧图，也可能是没拿到帧信息。
  assert.equal(buildGifControllerOptions({ frameCount: 1 }).frameCountKnown, false);

  // 页面必须按 frameCountKnown 决定是否显示说明（而不是按 frameCount > 1 自己算）。
  assert.match(pageSource, /options\.frameCountKnown/, '页面必须用 frameCountKnown 判断是否提示');
  assert.match(pageSource, /gifFrameHint/, '缺少帧数未知的说明状态');
  assert.match(pageSource, /帧数未知/, '必须说明帧数未知');

  // 控制器必须真的用上这些参数：12 帧的控制器 tick 一次只前进一帧。
  const ctrl = createGifPlaybackController(withFrames);
  assert.equal(ctrl.frameCount, 12);
  assert.equal(ctrl.tick(80), 1);
  assert.equal(ctrl.frameLabel, '2 / 12');
});

console.log(
  'SENSITIVE_IMAGE_PAGE 判据①②③④ 全部覆盖：两 Tab 独立分页 / 涉及行数不可退化为 0 / 字节与上游原文白名单净化 / 两 Tab key + GIF 控制器',
);
