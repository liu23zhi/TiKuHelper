// 「解密密钥」设置已移除（用户 2026-11-15 拍板方案 A：只删 UI，保留后端覆盖能力）。
//
// 这组用例钉两件事：
//   ① 页面上不得再有密钥输入/保存入口，也不得再引导用户「去换密钥」
//   ② 提示文案必须说清真实原因（库的来路不明），而不是让用户去找一个不存在的开关
//
// 为什么文案要单独钉：原本文案写「请到系统设置 → 考试客户端数据库密钥核对或更换密钥」，
// 而那个设置已经不存在了 —— 用户照着做只会找不到入口。这类「指向已删除功能的提示」
// 比不提示更糟。

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const pagePath = path.join(here, '..', 'pages', 'UserExamTool', 'index.vue');

async function readPage() {
  return readFile(pagePath, 'utf8');
}

test('页面不再有「解密密钥」设置区块', async () => {
  const source = await readPage();
  assert.doesNotMatch(
    source,
    /<ConsolePanel title="解密密钥"/,
    '「解密密钥」面板必须已移除 —— 内置口令开箱即用，不需要用户配置',
  );
  assert.doesNotMatch(source, /saveDecryptKey|decryptKeyInput/, '不得残留保存密钥的入口');
});

test('不得再引导用户「更换密钥 / 核对指纹」（那个设置已经不存在）', async () => {
  const source = await readPage();
  for (const forbidden of ['更换密钥', '核对指纹', '核对或更换']) {
    assert.ok(
      !source.includes(forbidden),
      `提示里不得出现「${forbidden}」—— 该设置已删除，照着做只会找不到入口`,
    );
  }
});

test('解不开时给出的原因必须是「库的来路」而不是「密钥错了」', async () => {
  const source = await readPage();
  assert.match(
    source,
    /不是考试客户端生成/,
    '必须说明「可能不是考试客户端生成的库」—— 这是最可能的真实原因',
  );
  assert.match(source, /尚未适配的客户端版本|文件已损坏/, '必须给出另外两种可能原因');
});
