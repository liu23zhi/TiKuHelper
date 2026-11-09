const { test, afterEach } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');

const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..');
require(path.join(root, 'tests/helpers/vue-dom.cjs'));
const { mount, flushPromises } = require('@vue/test-utils');
const { nextTick } = require('vue');

const SensitiveText = require(path.join(root, 'src/components/SensitiveText.vue'));

let wrapper = null;
async function settle() {
  await nextTick();
  await flushPromises();
  await nextTick();
}
function render(props = {}, options = {}) {
  wrapper = mount(SensitiveText, {
    props: { text: '', ...props },
    ...options,
    attachTo: document.body,
  });
  return wrapper;
}
afterEach(async () => {
  wrapper?.unmount();
  wrapper = null;
  await settle();
  document.body.replaceChildren();
});

const badge = () => wrapper.find('.sensitive-text__badge');
const value = () => wrapper.find('.sensitive-text__value');
const toggle = () => wrapper.find('.sensitive-text__toggle');
const MASK = '（敏感内容已遮挡：共 6 字）';

test('非管理员：渲染遮挡文本 + 徽章，没有展开按钮', async () => {
  render({ text: MASK, isSensitive: true, serverMasked: true, canReveal: false });
  await settle();
  assert.equal(value().text(), MASK);
  assert.equal(badge().exists(), true);
  assert.equal(badge().text(), '敏感词');
  // 普通用户没有任何查看入口 —— 这是需求「用户端做隐藏」的硬要求。
  assert.equal(toggle().exists(), false);
  assert.ok(wrapper.classes().includes('sensitive-text--masked'));
});

test('管理员：默认也是遮挡 + 「显示原文」按钮', async () => {
  render({ text: MASK, isSensitive: true, serverMasked: true, canReveal: true });
  await settle();
  assert.equal(value().text(), MASK, '管理员默认同样遮挡，不是默认展开');
  assert.equal(toggle().text(), '显示原文');
  assert.equal(toggle().attributes('aria-pressed'), 'false');
  assert.ok(wrapper.classes().includes('sensitive-text--masked'));
});

test('管理员点按钮：展开原文；再点：收回到遮挡', async () => {
  const w = render({
    text: '这是原文答案',
    isSensitive: true,
    serverMasked: false,
    canReveal: true,
    revealed: true,
  });
  await settle();
  assert.equal(value().text(), '这是原文答案');
  assert.equal(toggle().text(), '隐藏原文');
  assert.equal(toggle().attributes('aria-pressed'), 'true');
  assert.ok(!wrapper.classes().includes('sensitive-text--masked'));

  // 点「隐藏原文」必须发出收起事件。
  toggle().trigger('click');
  await settle();
  assert.deepEqual(w.emitted('update:revealed')?.at(-1), [false]);
  assert.deepEqual(w.emitted('hide')?.at(-1), []);

  // 点「显示原文」必须发出展开事件。
  await w.setProps({ revealed: false });
  await settle();
  toggle().trigger('click');
  await settle();
  assert.deepEqual(w.emitted('update:revealed')?.at(-1), [true]);
  assert.deepEqual(w.emitted('reveal')?.at(-1), []);
});

test('安全默认：revealed=true 但 canReveal=false 时仍强制遮挡', async () => {
  // 父组件状态写错（或状态残留）时，非管理员也拿不到原文。
  render({ text: '这是原文答案', isSensitive: true, serverMasked: false, canReveal: false, revealed: true });
  await settle();
  assert.notEqual(value().text(), '这是原文答案', '非管理员即使 revealed=true 也不能看到原文');
  assert.equal(toggle().exists(), false);
  assert.ok(wrapper.classes().includes('sensitive-text--masked'));
});

test('老后端（serverMasked=false）前端本地兜底遮挡，只报长度', async () => {
  render({ text: '这是原文答案', isSensitive: true, serverMasked: false, canReveal: true });
  await settle();
  const shown = value().text();
  assert.ok(!shown.includes('这是原文答案'), `不能显示原文：${shown}`);
  assert.ok(shown.includes('共 6 字'), `必须报可读长度：${shown}`);
});

test('未命中的行：原样显示，无徽章、无按钮', async () => {
  render({ text: '普通内容', isSensitive: false, canReveal: true });
  await settle();
  assert.equal(value().text(), '普通内容');
  assert.equal(badge().exists(), false);
  assert.equal(toggle().exists(), false);
  assert.ok(!wrapper.classes().includes('sensitive-text--masked'));
});

test('空文本在命中态下显示占位而不是空白', async () => {
  render({ text: '', isSensitive: true, serverMasked: true, canReveal: false });
  await settle();
  assert.ok(value().text().includes('原文为空'), `空文本必须有占位：${value().text()}`);
});

test('loading 时按钮禁用，阻止重复请求原文', async () => {
  render({ text: MASK, isSensitive: true, serverMasked: true, canReveal: true, loading: true });
  await settle();
  assert.equal(toggle().attributes('disabled'), '');
});

test('徽章 tooltip 区分「已遮挡」与「已展开」，标记本身不变', async () => {
  const w = render({ text: MASK, isSensitive: true, serverMasked: true, canReveal: false });
  await settle();
  const anonTitle = badge().attributes('title');
  assert.ok(anonTitle.includes('仅管理员可查看原文'), anonTitle);

  await w.setProps({ canReveal: true, revealed: true });
  await settle();
  assert.equal(badge().text(), '敏感词', '展开后标记必须还在');
  assert.ok(badge().attributes('title').includes('当前已显示原文'), badge().attributes('title'));
});
