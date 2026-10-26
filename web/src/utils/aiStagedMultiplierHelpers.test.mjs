import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import {
  createStagedPointRowKey,
  getStagedMultiplierPointsError,
  insertStagedMultiplierPoint,
} from './aiStagedMultiplierHelpers.mjs';
import { buildAISearchConfigSavePayload } from './aiSearchModelHelpers.mjs';

const start = () => ({ tokens: 0, multiplier: 4 });

test('stage validation accepts small second thresholds and configurable tails', () => {
  for (const tokens of [2, 99999, 100000, 100001, 1000000000]) {
    for (const multiplier of [0.3, 0.65, 1, 4]) {
      assert.equal(getStagedMultiplierPointsError([start(), { tokens: 1, multiplier: 4 }, { tokens, multiplier }]), '');
    }
  }
});

test('stage validation removes fixed first multiplier and business value bounds', () => {
  const points = [
    { tokens: 0, multiplier: 12.5 },
    { tokens: 2000000000, multiplier: 12.5 },
    { tokens: Number.MAX_SAFE_INTEGER, multiplier: -3.25 },
  ];
  assert.equal(getStagedMultiplierPointsError(points), '');
});

test('stage validation accepts more than twenty nodes', () => {
  const points = Array.from({ length: 25 }, (_, index) => ({ tokens: index, multiplier: 25 - index }));
  assert.equal(getStagedMultiplierPointsError(points), '');
  const inserted = insertStagedMultiplierPoint(points);
  assert.equal(inserted.length, 26);
});

test('stage validation accepts a single zero-token node as a flat multiplier', () => {
  assert.equal(getStagedMultiplierPointsError([{ tokens: 0, multiplier: 2.75 }]), '');
  assert.equal(getStagedMultiplierPointsError([]), '阶段倍率价格表至少需要保留首个 0 token 节点');
});

test('stage thresholds preserve exact int64-range decimal strings', () => {
  const points = [
    { tokens: '0', multiplier: 2 },
    { tokens: '9007199254740993', multiplier: 1 },
    { tokens: '9223372036854775807', multiplier: 0 },
  ];
  assert.equal(getStagedMultiplierPointsError(points), '');
  assert.ok(getStagedMultiplierPointsError([
    { tokens: '0', multiplier: 2 },
    { tokens: '9223372036854775808', multiplier: 1 },
  ]));
  assert.ok(getStagedMultiplierPointsError([{ tokens: '1.0', multiplier: 1 }]));
});

test('inserted stage threshold is computed exactly above Number safe integer range', () => {
  const points = [
    { tokens: '0', multiplier: 2 },
    { tokens: '9007199254740995', multiplier: 1 },
  ];
  const inserted = insertStagedMultiplierPoint(points);
  assert.deepEqual(inserted.map(point => point.tokens), ['0', '4503599627370497', '9007199254740995']);
  assert.equal(getStagedMultiplierPointsError(inserted), '');
});

test('stage validation rejects invalid counts, thresholds, ordering and multipliers', () => {
  const invalid = [
    null, [],
    [{ tokens: 1, multiplier: 4 }, { tokens: 2, multiplier: 3 }],
    [start(), { tokens: 0, multiplier: 3 }],
    [start(), { tokens: -1, multiplier: 3 }],
    [start(), { tokens: 1.5, multiplier: 3 }],
    [start(), { tokens: NaN, multiplier: 3 }],
    [start(), { tokens: Number.MAX_SAFE_INTEGER + 1, multiplier: 3 }],
    [start(), { tokens: null, multiplier: 3 }],
    [start(), { tokens: '', multiplier: 3 }],
    [start(), { tokens: 2, multiplier: 3 }, { tokens: 1, multiplier: 2 }],
    [start(), { tokens: 1, multiplier: 2 }, { tokens: 2, multiplier: 3 }],
    ...[Infinity, NaN, null, ''].map(multiplier => [start(), { tokens: 1, multiplier }]),
  ];
  for (const points of invalid) {
    assert.ok(getStagedMultiplierPointsError(points), `must reject ${JSON.stringify(points)}`);
  }
});

test('save payload preserves zero and high-precision staged multipliers', () => {
  const points = [
    { tokens: '0', multiplier: 0 },
    { tokens: '9007199254740993', multiplier: 12.3456789 },
  ];
  assert.deepEqual(buildAISearchConfigSavePayload({ stagedTokenMultiplierPoints: points }, '').stagedTokenMultiplierPoints, points);
});

test('inserting a node uses a valid integer gap without changing existing objects', () => {
  for (const points of [
    [start(), { tokens: 2, multiplier: 2 }],
    [start(), { tokens: 8, multiplier: 2 }, { tokens: 9, multiplier: 1 }],
    [start(), { tokens: 3, multiplier: 2 }, { tokens: 1000000000, multiplier: 0.65 }],
  ]) {
    const snapshot = structuredClone(points);
    const inserted = insertStagedMultiplierPoint(points);
    assert.equal(inserted.length, points.length + 1);
    assert.equal(getStagedMultiplierPointsError(inserted), '');
    assert.deepEqual(points, snapshot);
    for (const point of points) assert.ok(inserted.includes(point));
    assert.equal(inserted.at(-1), points.at(-1));
  }
});

test('consecutive integer thresholds append a valid tail instead of duplicating a node', () => {
  const points = [start(), { tokens: 1, multiplier: 1 }];
  const inserted = insertStagedMultiplierPoint(points);
  assert.deepEqual(inserted.map(point => point.tokens), [0, 1, '2']);
  assert.equal(inserted.at(-1).multiplier, 1);
  assert.equal(getStagedMultiplierPointsError(inserted), '');
});

test('insertion refuses invalid drafts', () => {
  assert.throws(() => insertStagedMultiplierPoint([start(), { tokens: 0, multiplier: 3 }]));
});

test('row keys survive editable threshold changes and never collide after deletion', () => {
  const rowKey = createStagedPointRowKey();
  const first = start();
  const second = { tokens: 1, multiplier: 2 };
  const firstKey = rowKey(first);
  const secondKey = rowKey(second);
  second.tokens = 5;
  assert.equal(rowKey(first), firstKey);
  assert.equal(rowKey(second), secondKey);
  assert.notEqual(firstKey, secondKey);
  assert.notEqual(rowKey({ tokens: 5, multiplier: 2 }), secondKey);
  assert.deepEqual(buildAISearchConfigSavePayload({ stagedTokenMultiplierPoints: [first, second] }, '').stagedTokenMultiplierPoints, [
    { tokens: '0', multiplier: 4 },
    { tokens: '5', multiplier: 2 },
  ]);
});

test('AI search config delegates staged editing to the shared helpers', () => {
  const source = readFileSync(new URL('../pages/AISearchConfig/index.vue', import.meta.url), 'utf8');
  assert.match(source, /getStagedMultiplierPointsError/);
  assert.match(source, /insertStagedMultiplierPoint/);
  assert.match(source, /createStagedPointRowKey/);
  assert.match(source, /:row-key="stagedPointRowKey"/);
  assert.match(source, /#bodyCell="\{ column, record, index \}"[\s\S]*?<a-input v-if="column\.key === 'tokens'" v-model:value="record\.tokens" inputmode="numeric"/);
  assert.match(source, /index === 0 \|\| formState\.stagedTokenMultiplierPoints\.length <= 1/);
});

test('deep-thinking capability modal displays search capability details instead of API usage instructions', async () => {
  const source = await readFile(new URL('../pages/AISearchConfig/index.vue', import.meta.url), 'utf8');
  const dialog = await readFile(new URL('../pages/AISearchConfig/AISearchCapabilityDialog.vue', import.meta.url), 'utf8');
  for (const expected of [
    'capability-detail-panel',
    '支持的能力',
    '默认结果数',
    '最大结果数',
    '支持的参数',
    'engine.description',
  ]) {
    assert.ok(dialog.includes(expected), 'missing search capability detail: ' + expected);
  }
  assert.match(source, /v-model:open="capabilityModalVisible"/);
  assert.match(source, /:detail="searchEngineDetail"/);
  assert.match(source, /features: Array\.isArray\(response\.engine\.features\)/);
  assert.doesNotMatch(dialog, /API 端点/);
  assert.doesNotMatch(dialog, /https:\/\/uapis\.cn\/docs\/api-reference/);
  assert.doesNotMatch(dialog, /capability-response-example/);
});

test('AI pricing card header wraps long copy and actions instead of overlapping', async () => {
  const source = await readFile(new URL('../pages/AISearchConfig/index.vue', import.meta.url), 'utf8');
  const panel = await readFile(new URL('../components/ConsolePanel.vue', import.meta.url), 'utf8');
  assert.match(source, /\.sub-card-subtitle\s*\{[^}]*overflow-wrap:\s*anywhere/s);
  assert.match(source, /<ConsolePanel title="模型价格表"/);
  assert.match(panel, /flex-wrap:\s*wrap/);
});
