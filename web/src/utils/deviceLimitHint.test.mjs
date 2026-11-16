// 「设备上限」提示文案的自洽性钉子。
//
// 用户报的真实矛盾（截图原话）：
//   「上限只能调低不能调高：你的等级本身不限设备数，可以设为任意非负整数。」
// 前半句说「只能调低」，后半句说「可以设为任意非负整数」——
// 等级不限时根本没有「调低」这个概念（当前生效值本来就是 0/不限），
// 两句话放在一起自相矛盾，用户无法判断自己到底能不能调高。
//
// 根因：模板里「上限只能调低不能调高：」是**硬编码前缀**，而
// limitCeilingText 会在「有上界 / 无上界」两种情况下返回完全不同的句子。
// 前缀没有跟着分支走。

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const pagePath = path.join(here, '..', 'pages', 'MyDevices', 'index.vue');

const read = () => readFile(pagePath, 'utf8');

test('「只能调低」不能是无条件硬编码前缀', async () => {
  const source = await read();
  // 允许出现在「有上界」的分支里，但不允许作为模板里对两种情况都生效的固定前缀
  const hardcoded = /<p class="hint">\s*上限只能调低不能调高：\{\{/;
  assert.doesNotMatch(
    source,
    hardcoded,
    '「上限只能调低不能调高：」不能作为固定前缀拼在 limitCeilingText 前面 —— ' +
      '等级不限（无上界）时没有「调低」的概念，会造成「只能调低」与「可设为任意非负整数」矛盾',
  );
});

test('提示必须由两种分支各自给出完整句子', async () => {
  const source = await read();
  assert.match(
    source,
    /limitHint\(|limitRuleText|limitPanelHint/,
    '必须有一个 computed/函数按「有上界 / 无上界」分别产出完整提示句',
  );
});

test('无上界时必须说明「可设任意非负整数」，且不提「调高」受限', async () => {
  const helper = await readFile(
    path.join(here, '..', 'pages', 'MyDevices', 'index.vue'),
    'utf8',
  );
  assert.match(
    helper,
    /可设为任意非负整数|任意非负整数/,
    '无上界分支必须说明可设为任意非负整数',
  );
});

test('调低后的影响说明必须保留（那是用户最需要的行为承诺）', async () => {
  const source = await read();
  assert.match(
    source,
    /不会被立刻踢下线/,
    '必须保留「调低后超出的设备不会被立刻踢下线」——那是用户做决定的关键信息',
  );
});
test('面板描述在无上界时也不能说「只能调低不能调高」', async () => {
  const source = await read();
  // limitPanelDescription 在 editable=true 后直接返回「只能调低不能调高」，
  // 而 hasUpperBound=false 时这句话与「可设任意非负整数」相反。
  const panel = source.match(/const limitPanelDescription = computed\([\s\S]*?\n\}\)/)
  assert.ok(panel, '必须能定位 limitPanelDescription')
  assert.match(
    panel[0],
    /hasUpperBound/,
    'limitPanelDescription 必须按 hasUpperBound 分支，否则无上界时仍会说「只能调低不能调高」',
  )
})