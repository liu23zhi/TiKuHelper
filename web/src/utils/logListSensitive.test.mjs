import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildSensitiveFields,
  buildSensitiveBadge,
  buildLogRowDetail,
  buildLogDetailSections,
  buildLogResultText,
} from './logListHelpers.mjs';

const hitWords = JSON.stringify([
  { scope: 'question', status: 'clean', forbidden: false, words: [] },
  { scope: 'answer', status: 'forbidden', forbidden: true, words: ['敏感词', '脏话'] },
]);

test('buildSensitiveFields reports unchecked when no checkedAt is present', () => {
  // 后端只在「检测过」的行下发字段；缺字段必须读成「未检测」，
  // 不能伪装成「检测通过」。
  assert.equal(buildSensitiveFields({}).state, 'unchecked');
  assert.equal(buildSensitiveFields({ sensitiveFlag: false }).state, 'unchecked');
  assert.equal(buildSensitiveFields({ sensitiveFlag: true }).state, 'unchecked');
});

test('buildSensitiveFields classifies hit / clean separately', () => {
  const hit = buildSensitiveFields({
    sensitiveFlag: true,
    sensitiveWords: hitWords,
    sensitiveCheckedAt: '2026-11-09T10:00:00Z',
  });
  assert.equal(hit.state, 'hit');
  assert.deepEqual(hit.words, ['敏感词', '脏话']);

  const clean = buildSensitiveFields({
    sensitiveFlag: false,
    sensitiveWords: JSON.stringify([{ scope: 'answer', status: 'clean', forbidden: false, words: [] }]),
    sensitiveCheckedAt: '2026-11-09T10:00:00Z',
  });
  assert.equal(clean.state, 'clean');
  assert.deepEqual(clean.words, []);
});

test('buildSensitiveBadge renders a red badge only for hits', () => {
  const hit = buildSensitiveBadge({
    sensitiveFlag: true,
    sensitiveWords: hitWords,
    sensitiveCheckedAt: '2026-11-09T10:00:00Z',
  });
  assert.equal(hit.state, 'hit');
  assert.equal(hit.label, '敏感词');
  assert.equal(hit.color, 'red');
  // tooltip 必须点明是哪一个对象命中
  assert.match(hit.title, /答案/);
  assert.match(hit.title, /敏感词/);

  const clean = buildSensitiveBadge({
    sensitiveFlag: false,
    sensitiveCheckedAt: '2026-11-09T10:00:00Z',
  });
  assert.equal(clean.label, '已检测');
  assert.equal(clean.color, 'default');

  const unchecked = buildSensitiveBadge({});
  assert.equal(unchecked.label, '');
  assert.equal(unchecked.state, 'unchecked');
});

test('buildSensitiveBadge surfaces partial failures without claiming a hit', () => {
  const badge = buildSensitiveBadge({
    sensitiveFlag: false,
    sensitiveCheckError: 'answer: uapi: HTTP 500',
    sensitiveWords: JSON.stringify([{ scope: 'question', status: 'clean', forbidden: false, words: [] }]),
    sensitiveCheckedAt: '2026-11-09T10:00:00Z',
  });
  assert.equal(badge.state, 'clean');
  assert.match(badge.title, /部分对象失败/);
  assert.match(badge.error, /500/);
});

test('buildSensitiveBadge tolerates malformed word payloads', () => {
  for (const raw of ['', 'not-json', '{}', '[]', '[null, 1, "x"]']) {
    const badge = buildSensitiveBadge({
      sensitiveFlag: true,
      sensitiveWords: raw,
      sensitiveCheckedAt: '2026-11-09T10:00:00Z',
    });
    assert.equal(badge.state, 'hit', `raw=${raw}`);
    assert.deepEqual(badge.words, [], `raw=${raw}`);
    // 没有具体词时也要有可展示的说明，而不是空 tooltip
    assert.ok(badge.title.length > 0, `raw=${raw}`);
  }
});

test('buildLogRowDetail keeps the original answer text and appends the mark', () => {
  const record = {
    module: 'search',
    description: '搜索完成 requestId=r-1 mode=normal use=wanneng answers=1',
    newAnswer: JSON.stringify({ answer: { bestAnswer: ['A'], answerKeyText: 'A' } }),
    sensitiveFlag: true,
    sensitiveWords: hitWords,
    sensitiveCheckedAt: '2026-11-09T10:00:00Z',
  };
  // 日志用于排障：详情必须保留原答案，不能被 masked_text 替换。
  assert.equal(buildLogResultText(record), 'A');
  assert.equal(buildLogRowDetail(record), '答案：A');
});

test('buildLogDetailSections appends a sensitive section without touching originals', () => {
  const sections = buildLogDetailSections({
    module: 'search',
    newAnswer: '{"answer":{"bestAnswer":["A"]}}',
    sensitiveFlag: true,
    sensitiveWords: hitWords,
    sensitiveCheckedAt: '2026-11-09T10:00:00Z',
  });
  const sensitive = sections.find((section) => section.key === 'sensitive');
  assert.ok(sensitive, '必须追加敏感词段落');
  assert.equal(sensitive.label, '敏感词检测');
  assert.match(sensitive.value, /敏感词/);
  // 原文段落必须原样保留
  const response = sections.find((section) => section.key === 'response');
  assert.equal(response.value, '{"answer":{"bestAnswer":["A"]}}');
});

test('buildLogDetailSections omits the sensitive section when unchecked', () => {
  const sections = buildLogDetailSections({ module: 'search', newAnswer: '{"status":"success"}' });
  assert.equal(sections.some((section) => section.key === 'sensitive'), false);
});
