function isNumericValue(value) {
  return (typeof value === 'number' || typeof value === 'string') && String(value).trim() !== '';
}

export function getStagedMultiplierPointsError(points) {
  if (!Array.isArray(points) || points.length < 1) {
    return '阶段倍率价格表至少需要保留首个 0 token 节点';
  }
  let previousTokens = -1;
  let previousMultiplier = null;
  for (const [index, point] of points.entries()) {
    const tokens = Number(point?.tokens);
    const multiplier = Number(point?.multiplier);
    if (!isNumericValue(point?.tokens) || !Number.isSafeInteger(tokens) || tokens < 0) {
      return `第 ${index + 1} 个节点的 token 阈值必须是非负安全整数`;
    }
    if (!isNumericValue(point?.multiplier) || !Number.isFinite(multiplier)) {
      return `第 ${index + 1} 个节点的倍率必须是有限数值`;
    }
    if (index === 0 && tokens !== 0) {
      return '阶段倍率价格表首个节点的 token 阈值必须为 0';
    }
    if (tokens <= previousTokens) {
      return '阶段倍率 token 阈值必须严格递增';
    }
    if (previousMultiplier !== null && multiplier > previousMultiplier) {
      return '阶段倍率必须随 token 数量增加而不升高';
    }
    previousTokens = tokens;
    previousMultiplier = multiplier;
  }
  return '';
}

export function insertStagedMultiplierPoint(points) {
  const error = getStagedMultiplierPointsError(points);
  if (error) throw new Error(error);
  const inserted = [...points];
  // Prefer a gap near the tail, preserving all existing thresholds and row identities.
  for (let index = points.length - 1; index > 0; index -= 1) {
    const previous = points[index - 1];
    const gap = Number(points[index].tokens) - Number(previous.tokens);
    if (gap > 1) {
      inserted.splice(index, 0, {
        tokens: Number(previous.tokens) + Math.floor(gap / 2),
        multiplier: Number(previous.multiplier),
      });
      return inserted;
    }
  }

  const last = points[points.length - 1];
  const tokens = Number(last.tokens) + 1;
  if (!Number.isSafeInteger(tokens)) throw new Error('没有可新增的安全整数阈值，请先调整现有节点');
  inserted.push({ tokens, multiplier: Number(last.multiplier) });
  return inserted;
}

export function createStagedPointRowKey() {
  const keys = new WeakMap();
  let nextKey = 0;
  return point => {
    if (!keys.has(point)) keys.set(point, `stage-${nextKey++}`);
    return keys.get(point);
  };
}
