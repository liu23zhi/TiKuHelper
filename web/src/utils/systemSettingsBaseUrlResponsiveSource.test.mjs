import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const web = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

test('SystemSettings website Base control has bounded responsive CSS/DOM source guards (not pixel-layout proof)', async () => {
  const source = await readFile(path.join(web, 'src/pages/SystemSettings/index.vue'), 'utf8');
  const row = source.match(/<a-space\b[^>]*class="base-url-controls"[^>]*>[\s\S]*?<\/a-space>/)?.[0];
  assert.ok(row, 'website Base inner Space must be locally scoped for its Antd flex items');
  assert.match(row, /\bwrap\b/, 'retain wrapping');
  assert.match(row, /v-model:value="formState\.siteBaseUrl"/, 'retain existing draft field');
  assert.match(row, /@click="fillBaseUrlFromBrowser"/, 'retain original browser-fill action');
  const css = source.match(/<style\s+scoped>([\s\S]*?)<\/style>/)?.[1];
  assert.ok(css, 'responsive rules stay scoped');
  const rule = selector => {
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const body = css.match(new RegExp(escaped + '\\s*\\{([^}]+)\\}'))?.[1];
    assert.ok(body, 'missing scoped rule: ' + selector);
    return body;
  };
  assert.match(rule('.base-url-controls'), /\bwidth:\s*100%\s*;/);
  const item = rule('.base-url-controls :deep(.ant-space-item)');
  assert.match(item, /\bmin-width:\s*0\s*;/, 'remove automatic flex-item minimum');
  assert.match(item, /\bmax-width:\s*100%\s*;/, 'bound Antd wrapper to available width');
  const input = rule('.base-url-input');
  assert.match(input, /(?:^|[;\s])width:\s*320px\s*;/, 'retain comfortable desktop width');
  assert.match(input, /\bmin-width:\s*0\s*;/, 'allow shrink within narrow wrapper');
  assert.match(input, /\bmax-width:\s*100%\s*;/, 'bound input to available width');
  assert.doesNotMatch(input, /\bmin-width:\s*320px\s*;/, 'do not restore fixed overflow minimum');
  console.log('CSS_DOM_SOURCE_GUARD bounded website Base group; Antd item min0/max100%; input320/min0/max100%; no real-pixel acceptance claimed');
});
