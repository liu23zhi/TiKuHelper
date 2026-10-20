const MAX_STAGED_TOKEN_THRESHOLD = 9223372036854775807n;

function isNumericValue(value) {
  return (typeof value === 'number' || typeof value === 'string') && String(value).trim() !== '';
}

function parseStagedTokenThreshold(value) {
  let text;
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || value < 0) return null;
    text = String(value);
  } else if (typeof value === 'string') {
    text = value.trim();
  } else {
    return null;
  }
  if (!/^\d+$/.test(text)) return null;
  const tokens = BigInt(text);
  return tokens <= MAX_STAGED_TOKEN_THRESHOLD ? tokens : null;
}

export function getStagedMultiplierPointsError(points) {
  if (!Array.isArray(points) || points.length < 1) {
    return '阶段倍率价格表至少需要保留首个 0 token 节点';
  }
  let previousTokens = -1n;
  let previousMultiplier = null;
  for (const [index, point] of points.entries()) {
    const tokens = parseStagedTokenThreshold(point?.tokens);
    const multiplier = Number(point?.multiplier);
    if (tokens === null) {
      return `第 ${index + 1} 个节点的 token 阈值必须是非负 int64 十进制整数`;
    }
    if (!isNumericValue(point?.multiplier) || !Number.isFinite(multiplier)) {
      return `第 ${index + 1} 个节点的倍率必须是有限数值`;
    }
    if (index === 0 && tokens !== 0n) {
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
    const previousTokens = parseStagedTokenThreshold(previous.tokens);
    const currentTokens = parseStagedTokenThreshold(points[index].tokens);
    const gap = currentTokens - previousTokens;
    if (gap > 1n) {
      inserted.splice(index, 0, {
        tokens: (previousTokens + gap / 2n).toString(),
        multiplier: Number(previous.multiplier),
      });
      return inserted;
    }
  }

  const last = points[points.length - 1];
  const tokens = parseStagedTokenThreshold(last.tokens) + 1n;
  if (tokens > MAX_STAGED_TOKEN_THRESHOLD) throw new Error('已达到服务端 int64 阈值上限，无法再追加节点');
  inserted.push({ tokens: tokens.toString(), multiplier: Number(last.multiplier) });
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
