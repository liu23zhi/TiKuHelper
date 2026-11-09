const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

// 纯函数契约测试：不依赖 DOM、不依赖网络、不依赖文档。
// Windows 下动态 import 必须用 file:// URL（否则报 ERR_UNSUPPORTED_ESM_URL_SCHEME）。
const modulePath = pathToFileURL(path.resolve(__dirname, '../../src/utils/sensitiveDisplay.mjs')).href;

// .mjs 只能用动态 import 加载；缓存成 Promise，各用例 await 同一份实例。
const loaded = import(modulePath);
const load = () => loaded;

test('默认遮挡：命中行 + 服务端已遮挡时直接显示服务端下发的占位', async () => {
  const { resolveSensitiveDisplayText } = await load();
  const record = { sensitiveFlag: true, sensitiveMasked: true };
  const result = resolveSensitiveDisplayText(record, '（敏感内容已遮挡：共 12 字）', {});
  assert.equal(result.masked, true);
  assert.equal(result.text, '（敏感内容已遮挡：共 12 字）');
});

test('老后端（无 sensitiveMasked）也会本地遮挡，绝不回退成原文', async () => {
  const { resolveSensitiveDisplayText } = await load();
  const original = '这是答案原文';
  const result = resolveSensitiveDisplayText({ sensitiveFlag: true }, original, {});
  assert.equal(result.masked, true);
  assert.ok(!result.text.includes(original), `遮挡结果不得含原文：${result.text}`);
  // 只报长度，且按码点计（6 个汉字 = 6，不是 18 字节）。
  assert.ok(result.text.includes('共 6 字'), `必须报可读长度：${result.text}`);
});

test('未命中行原样返回，不做任何遮挡', async () => {
  const { resolveSensitiveDisplayText } = await load();
  const result = resolveSensitiveDisplayText({ sensitiveFlag: false, sensitiveMasked: false }, '普通文本', {});
  assert.equal(result.masked, false);
  assert.equal(result.text, '普通文本');
});

test('管理员展开态不改写文本（原文由调用方补齐）', async () => {
  const { resolveSensitiveDisplayText } = await load();
  const result = resolveSensitiveDisplayText({ sensitiveFlag: true, sensitiveMasked: true }, '已取回的原文', { revealed: true });
  assert.equal(result.masked, false);
  assert.equal(result.text, '已取回的原文');
});

test('countRunes 按码点计数，与后端 utf8.RuneCountInString 一致', async () => {
  const { countRunes, buildLocalMaskPlaceholder } = await load();
  assert.equal(countRunes('敏感词'), 3);
  assert.equal(countRunes('abc'), 3);
  assert.equal(countRunes(''), 0);
  // 代理对（emoji）算 1 个码点，而 String.length 是 2。
  assert.equal(countRunes('😀'), 1);
  assert.equal('😀'.length, 2);
  assert.ok(buildLocalMaskPlaceholder('😀').includes('共 1 字'));
});

test('空原文的占位不伪造长度', async () => {
  const { buildLocalMaskPlaceholder } = await load();
  assert.ok(buildLocalMaskPlaceholder('').includes('原文为空'));
  assert.ok(buildLocalMaskPlaceholder('   ').includes('原文为空'));
});

test('parseSensitiveScopes 容忍脏数据，不让整页报错', async () => {
  const { parseSensitiveScopes, buildSensitiveSummary } = await load();
  for (const raw of ['', 'not-json', '{}', 'null', '[1,2]']) {
    assert.deepEqual(parseSensitiveScopes(raw), [], `脏输入 ${raw} 必须返回空`);
  }
  const scopes = parseSensitiveScopes('[{"scope":"answer","forbidden":true,"words":["＊＊＊"]}]');
  assert.equal(scopes.length, 1);
  assert.equal(scopes[0].forbidden, true);

  const summary = buildSensitiveSummary('[{"scope":"question","forbidden":true,"words":["＊＊"]},{"scope":"answer","forbidden":false,"words":[]}]');
  assert.deepEqual(summary.groups, ['题目']);
  assert.equal(summary.wordCount, 1);
});

test('徽章文案随角色变化，但标记本身永远保留', async () => {
  const { buildSensitiveBadgeState } = await load();
  const record = { sensitiveFlag: true, sensitiveWords: '[{"scope":"answer","forbidden":true,"words":["＊＊＊"]}]' };

  const anon = buildSensitiveBadgeState(record, { isAdmin: false });
  assert.equal(anon.label, '敏感词');
  assert.ok(anon.title.includes('仅管理员可查看原文'));

  const adminCollapsed = buildSensitiveBadgeState(record, { isAdmin: true, revealed: false });
  assert.equal(adminCollapsed.label, '敏感词');
  assert.ok(adminCollapsed.title.includes('可点「显示原文」'));

  const adminRevealed = buildSensitiveBadgeState(record, { isAdmin: true, revealed: true });
  assert.equal(adminRevealed.label, '敏感词');
  assert.ok(adminRevealed.title.includes('当前已显示原文'));

  // 未命中：没有徽章。
  assert.equal(buildSensitiveBadgeState({ sensitiveFlag: false }, {}).label, '');
});

test('只有管理员且确实命中时才给展开按钮', async () => {
  const { canRevealOriginal } = await load();
  assert.equal(canRevealOriginal({ sensitiveFlag: true }, { isAdmin: true }), true);
  assert.equal(canRevealOriginal({ sensitiveFlag: true }, { isAdmin: false }), false);
  assert.equal(canRevealOriginal({ sensitiveFlag: false }, { isAdmin: true }), false);
  assert.equal(canRevealOriginal({}, {}), false);
});

test('shouldMaskRecord / isServerMasked 只认布尔 true', async () => {
  const { shouldMaskRecord, isServerMasked } = await load();
  for (const value of [undefined, null, false, 'true', 1, {}]) {
    assert.equal(shouldMaskRecord({ sensitiveFlag: value }), false, `sensitiveFlag=${String(value)} 不该判命中`);
    assert.equal(isServerMasked({ sensitiveMasked: value }), false, `sensitiveMasked=${String(value)} 不该判已遮挡`);
  }
  assert.equal(shouldMaskRecord({ sensitiveFlag: true }), true);
  assert.equal(isServerMasked({ sensitiveMasked: true }), true);
});
