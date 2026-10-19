import assert from 'node:assert/strict';
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

test('stage validation rejects invalid counts, thresholds, ordering and multipliers', () => {
  const invalid = [
    null, [], [start()], Array.from({ length: 21 }, (_, tokens) => ({ tokens, multiplier: 4 })),
    [{ tokens: 1, multiplier: 4 }, { tokens: 2, multiplier: 3 }],
    [{ tokens: 0, multiplier: 3 }, { tokens: 2, multiplier: 3 }],
    [start(), { tokens: 0, multiplier: 3 }],
    [start(), { tokens: -1, multiplier: 3 }],
    [start(), { tokens: 1.5, multiplier: 3 }],
    [start(), { tokens: 1000000001, multiplier: 3 }],
    [start(), { tokens: NaN, multiplier: 3 }],
    [start(), { tokens: null, multiplier: 3 }],
    [start(), { tokens: '', multiplier: 3 }],
    [start(), { tokens: 2, multiplier: 3 }, { tokens: 1, multiplier: 2 }],
    [start(), { tokens: 1, multiplier: 2 }, { tokens: 2, multiplier: 3 }],
    ...[0.29, 4.01, Infinity, NaN, null, ''].map(multiplier => [start(), { tokens: 1, multiplier }]),
  ];
  for (const points of invalid) {
    assert.ok(getStagedMultiplierPointsError(points), `must reject ${JSON.stringify(points)}`);
  }
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
  assert.deepEqual(inserted.map(point => point.tokens), [0, 1, 2]);
  assert.equal(inserted.at(-1).multiplier, 1);
  assert.equal(getStagedMultiplierPointsError(inserted), '');
});

test('insertion refuses invalid drafts and the backend node limit', () => {
  assert.throws(() => insertStagedMultiplierPoint([start(), { tokens: 0, multiplier: 3 }]));
  assert.throws(() => insertStagedMultiplierPoint(Array.from({ length: 20 }, (_, tokens) => ({ tokens, multiplier: 4 }))));
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
  assert.deepEqual(buildAISearchConfigSavePayload({ stagedTokenMultiplierPoints: [first, second] }, '').stagedTokenMultiplierPoints, [first, second]);
});

test('AI search config delegates staged editing to the shared helpers', () => {
  const source = readFileSync(new URL('../pages/AISearchConfig/index.vue', import.meta.url), 'utf8');
  assert.match(source, /getStagedMultiplierPointsError/);
  assert.match(source, /insertStagedMultiplierPoint/);
  assert.match(source, /createStagedPointRowKey/);
  assert.match(source, /:row-key="stagedPointRowKey"/);
});
