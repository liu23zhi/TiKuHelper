/**
 * 工单图片缓存管理页的纯展示 helper（T-12）。
 *
 * 为什么单独抽一个 .mjs：本仓的 `npm run test:unit` 跑的是
 * `node --test src/utils/*.test.mjs`，而 `web/node_modules` 是空壳（无 vue），
 * 所以**页面逻辑里能被自动验证的部分必须是不依赖 vue 的纯函数**。
 * 页面 index.vue 只做「取值 → 交给这里格式化」。
 *
 * 四项对应任务书里「四个必须显示的数字」：
 *  ① formatCacheBytes    总占用（字节 → 人类可读）
 *  ② 待清理数            直接是数字，但 readCacheCount 挡掉 null/负数
 *  ③ scanStateText       按 scan_state 分组（pending/approved/sensitive/failed）
 *  ④ describeRetention   三个保留策略值
 */

/** 1 KiB 的字节数。用 1024 进制（与后端 GiB/MiB 口径一致，不用 1000）。 */
const KIB = 1024;
const MIB = KIB * 1024;
const GIB = MIB * 1024;
const TIB = GIB * 1024;

/**
 * 把字节数格式化成人类可读的字符串。
 *
 * 判据 ④ 的一部分：空库/未加载时入参是 null/undefined/负数，必须返回 '0 B'
 * 而不是 'NaN B' 或抛异常 —— 页面要能显示 0。
 *
 * 小数位的选择：< 1 KiB 显示整数字节（图片最小也是几百字节，小数无意义）；
 * 往上统一 2 位小数并去掉尾零（`1.5 MiB` 而不是 `1.50 MiB`）。
 */
export function formatCacheBytes(value) {
  const bytes = readCacheCount(value);
  if (bytes === null || bytes === 0) return '0 B';
  const units = [
    { limit: KIB, suffix: 'B', divisor: 1 },
    { limit: MIB, suffix: 'KiB', divisor: KIB },
    { limit: GIB, suffix: 'MiB', divisor: MIB },
    { limit: TIB, suffix: 'GiB', divisor: GIB },
    { limit: Infinity, suffix: 'TiB', divisor: TIB },
  ];
  const unit = units.find(candidate => bytes < candidate.limit);
  const scaled = bytes / unit.divisor;
  if (unit.divisor === 1) return `${bytes} B`;
  // toFixed(2) 后去尾零：1.50 → 1.5、2.00 → 2。
  const text = scaled.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
  return `${text} ${unit.suffix}`;
}

/**
 * 把任意输入读成「非负整数」，读不出来返回 null。
 *
 * 单独存在的理由：后端 JSON 里 totalBytes 可能是 number，也可能是字符串
 * （sqlite 的 SUM 在某些驱动下返回字符串）。页面必须两种都吃。
 * 负数是异常值，一律当 null 处理 —— 显示 -1 会让管理员以为数据坏了。
 */
export function readCacheCount(value) {
  if (value === null || value === undefined) return null;
  if (typeof value === 'boolean') return null;
  if (typeof value !== 'number' && typeof value !== 'string') return null;
  const text = String(value).trim();
  if (text === '') return null;
  const number = Number(text);
  if (!Number.isFinite(number) || number < 0) return null;
  return Math.trunc(number);
}

/** scan_state 的固定顺序与中文文案（页面按这个顺序渲染，避免桶的顺序随数据抖动）。 */
export const SCAN_STATE_ROWS = Object.freeze([
  { key: 'pending', label: '待检测', hint: '还没开始检测，图片暂不显示' },
  { key: 'approved', label: '已通过', hint: '唯一允许显示图片的状态' },
  { key: 'sensitive', label: '判定敏感', hint: '检出敏感内容，图片不显示' },
  { key: 'failed', label: '检测失败', hint: '没能得出结论（无密钥/超时/上游错误），按不显示处理' },
  { key: 'scanning', label: '检测中', hint: '正在调上游检测，图片暂不显示' },
  { key: 'other', label: '其它', hint: '未知状态（历史数据或未来新增），需人工核对' },
]);

/**
 * scan_state 的中文文案。
 *
 * 未知状态**原样返回**而不是显示「未知」：库里出现新状态时，
 * 显示原文才能让人去查是什么，显示「未知」等于把线索删掉。
 */
export function scanStateText(state) {
  const key = String(state ?? '').trim();
  if (!key) return '—';
  const row = SCAN_STATE_ROWS.find(candidate => candidate.key === key);
  return row ? row.label : key;
}

/**
 * 把后端的 scanStates 对象转成页面要渲染的行数组（含 0 项）。
 *
 * 判据 ④：空库时每个桶都要出现且显示 0，不能因为后端没回这个键就整行消失。
 * 判据 ③：四项固定出现；scanning / other 有值时也带出来（否则
 * 「分项之和 != 总数」无法解释）。
 */
export function buildScanStateRows(scanStates) {
  const source = scanStates && typeof scanStates === 'object' ? scanStates : {};
  return SCAN_STATE_ROWS
    .filter(row => {
      // 固定四项永远显示；scanning / other 只在有值时出现。
      if (row.key === 'pending' || row.key === 'approved' ||
        row.key === 'sensitive' || row.key === 'failed') return true;
      return (readCacheCount(source[row.key]) ?? 0) > 0;
    })
    .map(row => ({
      key: row.key,
      label: row.label,
      hint: row.hint,
      count: readCacheCount(source[row.key]) ?? 0,
    }));
}

/**
 * 保留策略的中文说明（四个数字里的第 ④ 项）。
 *
 * 三个值缺任何一个都回落到 `—`：显示 0 天 / 0 GiB 会被读成
 * 「已经该把图片删光了」，而真实含义是「没读到配置」。
 */
export function describeRetention(retention) {
  const source = retention && typeof retention === 'object' ? retention : {};
  const ageDays = readCacheCount(source.maxAgeDays);
  const totalGiB = readCacheCount(source.maxTotalGiB);
  const intervalMinutes = readCacheCount(source.cleanupIntervalMinutes);
  return {
    ageText: ageDays === null ? '—' : `${ageDays} 天`,
    sizeText: totalGiB === null ? '—' : `${totalGiB} GiB`,
    intervalText: intervalMinutes === null ? '—' : `${intervalMinutes} 分钟`,
    // 总量上限的字节形态：页面用它算「当前占用 / 上限」的占比条。
    limitBytes: readCacheCount(source.maxTotalBytes) ?? (
      totalGiB === null ? null : totalGiB * GIB
    ),
  };
}

/**
 * 计算「当前占用 / 上限」的百分比（0–100 的整数）。
 *
 * 上限未知或为 0 时返回 null（页面显示「—」而不是 0% 或 100%）：
 * 上限为 0 是「没读到配置」，不是「已经爆了」。这正是判据 ④
 * 要求的「空库显示 0 而非报错」在占比条上的对应形态。
 */
export function usagePercent(totalBytes, limitBytes) {
  const used = readCacheCount(totalBytes);
  const limit = readCacheCount(limitBytes);
  if (used === null || limit === null || limit === 0) return null;
  const percent = Math.round((used / limit) * 100);
  return percent > 100 ? 100 : percent;
}

/**
 * 判断一条记录当前能不能删（判据 ① 的前端闸门）。
 *
 * 与后端**同一个判据**：refCount > 0 就不给删。
 * 前端也挡一次的理由不是「信任前端」，而是让按钮在点之前就是禁用态 ——
 * 让管理员点了才收到 409 是坏体验，但**真正的保护在后端**（服务端逐条判定）。
 */
export function canDeleteBlob(item) {
  const refCount = readCacheCount(item?.refCount);
  if (refCount === null) return false;
  return refCount === 0;
}

/**
 * 「立即清理一次」的结果文案（判据 ③：两个数字必须分开出现）。
 *
 * 合成一个 total 会让管理员看不出是哪条策略生效了，所以文案里
 * 「按时间 N 条」「按大小 M 条」必须各自可见。
 */
export function describeCleanResult(payload) {
  const source = payload && typeof payload === 'object' ? payload : {};
  const agePurged = readCacheCount(source.agePurged);
  const sizePurged = readCacheCount(source.sizePurged);
  if (source.available === false) {
    return String(source.message ?? '').trim() || '清理能力尚未接线（等待 T-11）';
  }
  if (agePurged === null && sizePurged === null) {
    return String(source.message ?? '').trim() || '清理完成';
  }
  return `按时间清理 ${agePurged ?? 0} 条，按大小清理 ${sizePurged ?? 0} 条`;
}

/** 允许的媒体类型（页面筛选下拉）。与后端支持的 jpeg/png/gif/webp 对齐。 */
export const MEDIA_TYPE_OPTIONS = Object.freeze([
  { label: 'PNG', value: 'image/png' },
  { label: 'JPEG', value: 'image/jpeg' },
  { label: 'GIF', value: 'image/gif' },
  { label: 'WebP', value: 'image/webp' },
]);

/** 引用计数筛选下拉。value 与后端约定的 refFilter 取值一致。 */
export const REF_FILTER_OPTIONS = Object.freeze([
  { label: '全部', value: '' },
  { label: '仅待清理（ref_count = 0）', value: 'zero' },
  { label: '仅被引用（ref_count > 0）', value: 'referenced' },
]);

/**
 * 校验一条 sha256 是否为合法的 64 位小写十六进制。
 *
 * 与后端 normalizeTicketImageCacheSHA256 同一判据：非法值在发请求前就拒绝，
 * 避免把 /undefined、/NaN 这类路径打到后端。
 */
export function isValidSha256(value) {
  return typeof value === 'string' && /^[0-9a-f]{64}$/.test(value.trim().toLowerCase());
}

/**
 * 把用户的筛选输入整理成请求参数。
 *
 * 空值不出现在参数里（后端按「没传 = 不过滤」处理）：传 `mediaType=''`
 * 与不传在服务端等价，但会让浏览器缓存键不同、也让排查时的请求日志难读。
 */
export function buildListParams({ mediaType, scanState, refFilter, keyword } = {}) {
  const params = {};
  const media = String(mediaType ?? '').trim();
  if (media) params.mediaType = media;
  const state = String(scanState ?? '').trim();
  if (state) params.scanState = state;
  const ref = String(refFilter ?? '').trim();
  if (ref) params.refFilter = ref;
  const kw = String(keyword ?? '').trim().toLowerCase();
  if (kw) params.keyword = kw;
  return params;
}

/**
 * 从列表响应里取四项汇总（缺失一律回落到 0 / null，不抛异常）。
 *
 * 这一层存在的意义：页面绑定的必须是**已归一化**的值。
 * 直接把 `res.data.summary.scanStates.approved` 塞进模板，
 * 一旦后端少回一个键就会静默显示空白，而这四项是页面的核心产出。
 */
export function readSummary(body) {
  const summary = body && typeof body === 'object' && body.summary && typeof body.summary === 'object'
    ? body.summary
    : {};
  const totalBytes = readCacheCount(summary.totalBytes) ?? 0;
  const retention = describeRetention(summary.retention);
  return {
    totalCount: readCacheCount(summary.totalCount) ?? 0,
    totalBytes,
    totalBytesText: formatCacheBytes(totalBytes),
    zeroRefCount: readCacheCount(summary.zeroRefCount) ?? 0,
    scanRows: buildScanStateRows(summary.scanStates),
    scanStates: summary.scanStates && typeof summary.scanStates === 'object' ? summary.scanStates : {},
    retention,
    usagePercent: usagePercent(totalBytes, retention.limitBytes),
  };
}
