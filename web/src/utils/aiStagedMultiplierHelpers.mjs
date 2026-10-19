export const MAX_AI_STAGED_POINTS = 20;
export const MAX_AI_STAGED_TOKENS = 1000000000;

function isNumericValue(value) {
  return (typeof value === 'number' || typeof value === 'string') && String(value).trim() !== '';
}

export function getStagedMultiplierPointsError(points) {
  if (!Array.isArray(points) || points.length < 2 || points.length > MAX_AI_STAGED_POINTS) {
    return '阶段倍率价格表需要 2～20 个节点';
  }
  let previousTokens = -1;
  let previousMultiplier = 4;
  for (const [index, point] of points.entries()) {
    const tokens = Number(point?.tokens);
    const multiplier = Number(point?.multiplier);
    if (!isNumericValue(point?.tokens) || !Number.isSafeInteger(tokens) || tokens < 0 || tokens > MAX_AI_STAGED_TOKENS) {
      return `第 ${index + 1} 个节点的 token 阈值必须是 0～1000000000 的整数`;
    }
    if (!isNumericValue(point?.multiplier) || !Number.isFinite(multiplier) || multiplier < 0.3 || multiplier > 4) {
      return `第 ${index + 1} 个节点的倍率必须在 0.3～4 之间`;
    }
    const roundedMultiplier = Math.round(multiplier * 1000000) / 1000000;
    if (index === 0 && (tokens !== 0 || roundedMultiplier !== 4)) {
      return '阶段倍率价格表必须从 0 token / 4x 开始';
    }
    if (tokens <= previousTokens) {
      return '阶段倍率 token 阈值必须严格递增';
    }
    if (roundedMultiplier > previousMultiplier) {
      return '阶段倍率必须随 token 数量增加而不升高';
    }
    previousTokens = tokens;
    previousMultiplier = roundedMultiplier;
  }
  return '';
}

export function insertStagedMultiplierPoint(points) {
  const error = getStagedMultiplierPointsError(points);
  if (error) throw new Error(error);
  if (points.length >= MAX_AI_STAGED_POINTS) throw new Error('阶段倍率价格表最多支持 20 个节点');

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
  if (tokens > MAX_AI_STAGED_TOKENS) throw new Error('没有可新增节点的整数阈值，请先调整现有节点');
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
