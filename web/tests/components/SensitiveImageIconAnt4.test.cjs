const { test, afterEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..');
require(path.join(root, 'tests/helpers/vue-dom.cjs'));
const { mount, flushPromises } = require('@vue/test-utils');
const { nextTick } = require('vue');

const Icon = require(path.join(root, 'src/components/SensitiveImageIcon.vue'));

let wrapper = null;
async function settle() {
  await nextTick();
  await flushPromises();
  await nextTick();
}
afterEach(async () => {
  wrapper?.unmount();
  wrapper = null;
  await settle();
  document.body.replaceChildren();
});

// 用户指定的源文件。测试只在它存在时比对 path（文档/素材可能不在仓库里，
// 测试不得依赖文档 —— 断言本身不读它也能全部通过）。
const svgSource = path.resolve(root, '../../../task_doc/tupiansunhuai.svg');

test('logo 使用用户指定的 viewBox（1271×1024，不许压成 64×64）', async () => {
  wrapper = mount(Icon, { attachTo: document.body });
  await settle();
  const svg = wrapper.find('svg');
  assert.equal(svg.exists(), true);
  assert.equal(svg.attributes('viewBox'), '0 0 1271 1024');
});

test('只有一条 path，且用 currentColor 而非硬编码颜色', async () => {
  wrapper = mount(Icon, { attachTo: document.body });
  await settle();
  const paths = wrapper.findAll('path');
  assert.equal(paths.length, 1, '用户提供的 SVG 是单条 path');
  const fill = paths[0].attributes('fill');
  assert.equal(fill, 'currentColor', '必须跟随主题色，禁止硬编码颜色');
  // 不允许残留 stroke 描边属性 —— 这个图标是实心填充风格。
  assert.equal(paths[0].attributes('stroke'), undefined);
  assert.equal(paths[0].attributes('stroke-width'), undefined);
});

test('可访问性语义：role=img + aria-label，且不回读路径', async () => {
  wrapper = mount(Icon, { props: { label: '敏感图片已遮挡' }, attachTo: document.body });
  await settle();
  const svg = wrapper.find('svg');
  assert.equal(svg.attributes('role'), 'img');
  assert.equal(svg.attributes('aria-label'), '敏感图片已遮挡');
  assert.equal(svg.attributes('aria-hidden'), 'true');
  assert.equal(svg.attributes('focusable'), 'false');
});

test('label 可传不同文案（敏感 / 检测中 / 检测失败是三种 UI 语义）', async () => {
  wrapper = mount(Icon, { props: { label: '检测中' }, attachTo: document.body });
  await settle();
  assert.equal(wrapper.find('svg').attributes('aria-label'), '检测中');
  await wrapper.setProps({ label: '检测失败' });
  await settle();
  assert.equal(wrapper.find('svg').attributes('aria-label'), '检测失败');
});

test('模板里不能出现 DOCTYPE / XML 声明（那是合法 SFC 的致命错误）', async () => {
  const source = fs.readFileSync(path.join(root, 'src/components/SensitiveImageIcon.vue'), 'utf8');
  // 只看 <template> 块，且先剥掉注释：注释里提到 DOCTYPE 是说明，不是声明。
  const template = source.match(/<template>([\s\S]*?)<\/template>/)?.[1] || '';
  const markup = template.replace(/<!--[\s\S]*?-->/g, '');
  assert.equal(/<!DOCTYPE/i.test(markup), false, 'SVG 的 DOCTYPE 不能进 Vue 模板');
  assert.equal(/<\?xml/i.test(markup), false, 'XML 声明不能进 Vue 模板');
  // 整个文件里也不许出现真正的声明（注释中提及可以）。
  const withoutComments = source.replace(/<!--[\s\S]*?-->/g, '');
  assert.equal(/<!DOCTYPE/i.test(withoutComments), false);
});

test('path d 与用户提供的源文件逐字节一致（源文件存在时才比对）', async () => {
  wrapper = mount(Icon, { attachTo: document.body });
  await settle();
  const rendered = wrapper.find('path').attributes('d');
  assert.ok(rendered && rendered.length > 200, 'path 必须是完整的长路径');
  assert.ok(rendered.startsWith('M710.09113 896'), `path 起点必须与源文件一致：${rendered.slice(0, 40)}`);

  if (!fs.existsSync(svgSource)) {
    // 素材不在本仓库时就到此为止：测试不得依赖文档/素材文件。
    return;
  }
  const source = fs.readFileSync(svgSource, 'utf8');
  const match = source.match(/<path[^>]*\sd="([^"]+)"/);
  assert.ok(match, '源文件必须包含 path');
  assert.equal(rendered, match[1], 'path 必须与源文件完全一致，不许改写');
});
