// 敏感图片检测管理页的纯函数层（不依赖 vue / axios，node --test 可直接执行）。
//
// 为什么把逻辑单独抽出来而不是写在 index.vue 里：
// 页面级断言（「源码里有某字符串」）只能证明符号存在，证明不了语义正确。
// 本文件的每个导出都是可执行的行为单元，测试直接调用它们断言输出，
// 因此「把 affectedRows 写死成 0」「把字节字段透传出去」这类改动会真的变红。
//
// 与后端 controller/sensitive_image.go 的契约：
//   GET  /adapter-service/sensitive-image/entries   列表（status / imageSha256 / page / pageSize）
//   GET  /adapter-service/sensitive-image/quota     额度配置 + 可选 userId 的当日用量
//   POST /adapter-service/sensitive-image/quota     改额度配置（指针语义：只改传入的键）
//   POST /adapter-service/sensitive-image/detect    手动触发（imageSha256 或 ticketId）

// ---- 两个 Tab ----

// Tab key 常量单独导出：页面模板与测试都引用这里，
// 避免「测试断言的是 'quota'、页面写的是 'exceeded'」这种两边各写一份的漂移。
export const SENSITIVE_IMAGE_TAB_QUOTA = 'quota';
export const SENSITIVE_IMAGE_TAB_RECORDS = 'records';

// 两个 Tab 的唯一定义源（顺序即 UI 顺序）。
//
// ⚠️ 「超额未检测」的真实数据源**不在缓存表**：额度耗尽时后端根本没写
// sensitive_image_cache_entries（sensitive_image_detect.go:302-309 直接 return）。
// controller/sensitive_image.go:52-53 的注释承诺了「见下方 quotaExceeded 分支」，
// 但那个分支**不存在**（整个文件没有读取 ticket_attachments.scan_state 的查询）。
// 所以本页把该 Tab 实现为「未得出可信结论的检测记录」（status=error，可切 pending），
// 并在页面上如实说明这一限制，而不是假装列出了额度耗尽的图。
export const SENSITIVE_IMAGE_TABS = Object.freeze([
  Object.freeze({
    key: SENSITIVE_IMAGE_TAB_QUOTA,
    label: '超额未检测',
    description:
      '未得出可信结论的图片（上次外呼没跑成 / 仍在检测中）。因额度耗尽而从未写入缓存的图不在此表 —— 后端尚无该查询通道。',
  }),
  Object.freeze({
    key: SENSITIVE_IMAGE_TAB_RECORDS,
    label: '全部检测记录',
    description: '按图片内容（SHA256）缓存的全部判定结论。同一张图被多张工单引用时只有一行。',
  }),
]);

// ⚠️ 上面 records Tab 里的 `sensitive` 结论有**两种来源，当前后端无法区分**：
//
//   1. 上游真的判定为敏感（provider=uapi，reason 是上游给的可解释结论）；
//   2. **额度耗尽时的默认标记** —— sensitive_image_detect.go:305-307 在把
//      State 设成 sensitive_by_quota 的同时，把 ScanState 也设成 "sensitive"，
//      与真违规的 scan_state 是**同一个值**；entity/ticket_attachment.go 里
//      没有任何列（Quota / Reason 均不存在）能区分这两者。
//
// 后果：管理员在「全部检测记录」看到一批 sensitive，分不清哪些是真违规、
// 哪些只是「没检测就按敏感处理」。而两者的处置动作**完全相反**：
// 前者要封禁/下架，后者补额度重检即可。
//
// 本页的处置：用 reason 与 provider 两列辅助人工判断，但**不宣称能精确区分**，
// 也不提供「按来源批量筛选」的假功能 —— 那需要后端新增列，不在前端范围。
// 这条注释留在 helper 里而不是只写在页面：将来有人想加「只 show 真违规」的
// 筛选器时，必须先看到这一段，否则会做出一个恒等于「全部」的筛选。


// TAB_QUOTA 的默认子状态：error 表示「上次外呼没跑成」，即最需要人工复核的一类。
export const SENSITIVE_IMAGE_QUOTA_TAB_STATUS = 'error';
export const SENSITIVE_IMAGE_QUOTA_TAB_STATUS_OPTIONS = Object.freeze([
  Object.freeze({ label: '检测失败（error）', value: 'error' }),
  Object.freeze({ label: '检测中（pending）', value: 'pending' }),
]);

// ---- 参数构造 ----

// readPositiveInt 把任意输入收敛成正整数，非法一律 null（绝不返回 NaN/0 冒充有效值）。
export function readPositiveInt(value) {
  if (!['number', 'string'].includes(typeof value)) return null;
  const text = String(value).trim();
  if (!/^[0-9]+$/.test(text)) return null;
  const number = Number(text);
  return Number.isSafeInteger(number) && number > 0 ? number : null;
}

// normalizeSha256 把输入归一成小写 64 位十六进制；非法返回空串。
//
// 前端做这一层不是为了替代后端校验（后端 isSensitiveImageSHA256 才是终点），
// 而是避免把 'abc'、'   ' 这类明显非法的值打到网络上换来一个 400 弹窗。
export function normalizeSha256(value) {
  const text = String(value ?? '').trim().toLowerCase();
  return /^[0-9a-f]{64}$/.test(text) ? text : '';
}

// buildEntryQuery 按 Tab 生成列表请求参数。
//
// 两个 Tab **各自独立分页**：调用方必须分别持有自己的 {page,pageSize}，
// 本函数不会在两个 Tab 之间共享或推断页码（共享页码会让「在 A Tab 翻到第 3 页、
// 切到 B Tab 后仍在第 3 页」这种串页行为出现）。
//
// ⚠️ 两个 Tab 不只参数不同，**语义与数据完整性都不同**，不要把它们当成
// 「同一份数据的两个筛选视图」：
//
//   - 「超额未检测」是**残差视图**：它只装得下 status=error / pending 这两类
//     「未得出可信结论」的记录。它的名字对应的是一个前端**装不满**的集合 ——
//     因额度耗尽而从未写进缓存表的图片（sensitive_image_detect.go:302-309
//     早退，不写 cache）根本不在这张表里，后端也没有按 scan_state 反查它们的接口。
//     所以这个 Tab 是「能查到的未决记录」，不是「全部超额记录」。页面必须
//     如实标注这个边界（见 index.vue 的 data-testid="quota-tab-limitation"）。
//
//   - 「全部检测记录」**刻意不带 status**：那才是「全部」。任何一个 status 值
//     （含空串）都会让它退化成筛选视图，而「全部」的意义就是不做任何结论筛选。
//     带 status='' 虽然会被后端 TrimSpace 成空串（等价不带），但显式不带的意图
//     更清楚 —— 也让「有人给它补了默认 status」这种改动在 diff 里一眼可见。
//
// 两者共用同一条 GET /sensitive-image/entries，但**不能因此认为它们的口径一致**：
// records 是「缓存表全量」，quota 是「缓存表里的未决子集」，而「额度耗尽」这一类
// 恰恰不在缓存表里。这也是为什么本页不提供「把 quota Tab 当成超额全量」的按钮。
export function buildEntryQuery(tabKey, options = {}) {
  const page = readPositiveInt(options.page) ?? 1;
  const rawSize = readPositiveInt(options.pageSize) ?? 20;
  const pageSize = rawSize > 100 ? 100 : rawSize;
  const params = { page, pageSize };
  const sha = normalizeSha256(options.imageSha256);
  if (sha) params.imageSha256 = sha;
  if (tabKey === SENSITIVE_IMAGE_TAB_QUOTA) {
    params.status = String(options.status ?? '').trim() || SENSITIVE_IMAGE_QUOTA_TAB_STATUS;
  }
  // 全部检测记录 Tab：这里**故意没有 else 分支**。缺了 else 才是对的，
  // 补一个 `params.status = ''` 会让「全部」带上一个空的筛选键。
  return params;
}

// buildDetectPayload 生成手动触发请求体。
//
// 语义与后端一致：两者都给时只处理 imageSha256，并显式回报「忽略了 ticketId」——
// 静默二选一会让管理员以为批量也跑了。
export function buildDetectPayload(input = {}) {
  const sha = normalizeSha256(input.imageSha256);
  const ticketId = readPositiveInt(input.ticketId);
  if (sha) {
    return {
      payload: { imageSha256: sha, ticketId: 0 },
      ignoredTicketId: ticketId !== null,
      error: '',
    };
  }
  if (ticketId !== null) {
    return { payload: { imageSha256: '', ticketId }, ignoredTicketId: false, error: '' };
  }
  return {
    payload: null,
    ignoredTicketId: false,
    error: '请填写 64 位图片标识（SHA256）或有效的工单编号',
  };
}

// ---- 响应投影：绝不回图片字节与上游原文 ----

// 手动触发响应允许出现的**全部**字段。白名单而不是黑名单：
// 黑名单（删掉 ciphertext）挡不住将来新增的 preview / thumbnail / bytes_b64。
export const SENSITIVE_IMAGE_DETECT_RESULT_FIELDS = Object.freeze([
  'imageSha256',
  'state',
  'cacheHit',
  'upstreamCalled',
  'charged',
  'usedCount',
  'dailyLimit',
  'perTicketLimit',
  'ticketCount',
  'reason',
  'ticketSubmitAllowed',
  'scanState',
]);

// 这些字段名一旦出现在投影结果里就是缺陷：它们承载图片字节或上游原文。
// 保留这张表是为了让「有人加了 preview 字段」时测试能指名道姓地报出是哪个。
export const SENSITIVE_IMAGE_FORBIDDEN_FIELDS = Object.freeze([
  'bytes', 'imageBytes', 'ciphertext', 'nonce', 'plain', 'plaintext', 'buffer',
  'base64', 'imageBase64', 'dataUrl', 'dataURL', 'preview', 'previewUrl', 'thumbnail',
  'originalText', 'original_text', 'rawResponse', 'raw', 'upstreamBody', 'upstreamResponse',
]);

// projectDetectResponse 把一次手动触发响应收敛成页面可用的安全视图。
//
// 输入是 controller 的原始 JSON：SHA 路径 {result, affectedAttachments, attachSyncError?}，
// 工单路径 {ticketId, total, detected, skipped, results[]}。
// 输出只保留白名单字段，且把「影响多少行」提取成显式的数值 —— 判据②要求
// 响应里写清本次涉及多少行，因为同一 SHA256 会被多张工单引用，只改一行的
// 展示会让人误判。
export function projectDetectResponse(payload) {
  const body = payload && typeof payload === 'object' ? payload : {};
  const rawResult = body.result && typeof body.result === 'object' ? body.result : {};
  const result = {};
  for (const field of SENSITIVE_IMAGE_DETECT_RESULT_FIELDS) {
    if (Object.hasOwn(rawResult, field)) result[field] = rawResult[field];
  }
  const view = {
    mode: Object.hasOwn(body, 'affectedAttachments') ? 'image' : 'ticket',
    result,
    // 影响行数三态：number=后端明确回报；null=后端没回这个字段（不能当 0）。
    affectedAttachments: readNonnegativeIntOrNull(body.affectedAttachments),
    attachSyncError: typeof body.attachSyncError === 'string' ? body.attachSyncError : '',
    ticketId: readNonnegativeIntOrNull(body.ticketId),
    total: readNonnegativeIntOrNull(body.total),
    detected: readNonnegativeIntOrNull(body.detected),
    skipped: readNonnegativeIntOrNull(body.skipped),
    resultCount: Array.isArray(body.results) ? body.results.length : null,
  };
  // 工单路径的 results[] 逐项投影，同样只留白名单字段。
  view.results = Array.isArray(body.results)
    ? body.results.map((item) => {
        const one = {};
        for (const field of SENSITIVE_IMAGE_DETECT_RESULT_FIELDS) {
          if (item && Object.hasOwn(item, field)) one[field] = item[field];
        }
        return one;
      })
    : [];
  return view;
}

// readNonnegativeIntOrNull 区分「0」与「没有这个字段」。
//
// 这两种情况在 UI 上必须不同：0 行是真实的「没有任何工单引用这张图」，
// 缺字段是「后端没回报」—— 把后者显示成 0 会让管理员以为改了个寂寞。
function readNonnegativeIntOrNull(value) {
  if (typeof value === 'number') {
    return Number.isSafeInteger(value) && value >= 0 ? value : null;
  }
  if (typeof value === 'string' && /^[0-9]+$/.test(value.trim())) {
    const number = Number(value.trim());
    return Number.isSafeInteger(number) ? number : null;
  }
  return null;
}

// describeDetectOutcome 生成给管理员看的一句话结论。
//
// 判据②的落点：无论哪条路径，都必须出现「本次涉及多少行/张」。
// 数字缺失时写「未知」，绝不写 0。
export function describeDetectOutcome(view) {
  const safe = view && typeof view === 'object' ? view : {};
  const state = String(safe.result?.state ?? '').trim();
  const stateText = state ? `结论 ${state}` : '未拿到结论';
  if (safe.mode === 'ticket') {
    const total = safe.total;
    const detected = safe.detected;
    const skipped = safe.skipped;
    if (total === null && detected === null) {
      return `${stateText}；本次涉及的图片张数未知（后端未回传 total）。`;
    }
    const totalText = total === null ? '未知' : String(total);
    const detectedText = detected === null ? '未知' : String(detected);
    const skippedText = skipped === null ? '未知' : String(skipped);
    return `${stateText}；本工单共 ${totalText} 张，重新检测 ${detectedText} 张，跳过 ${skippedText} 张（字节取不到）。`;
  }
  const rows = safe.affectedAttachments;
  if (rows === null) {
    return `${stateText}；本次涉及的附件行数未知（后端未回传 affectedAttachments）。`;
  }
  return `${stateText}；结论按 SHA256 生效，本次同步更新了 ${rows} 行附件 —— 引用这张图的所有工单都已同步。`;
}

// ---- GIF 播放控制器 ----

// createGifPlaybackController 是动图预览的播放控制状态机（纯函数，无 DOM）。
//
// 为什么需要它：动图缩略图默认自动播放会带来两个问题 ——
//   1. 审核时想停在某一帧看清内容，需要「暂停」；
//   2. 列表里多张动图同时播放会持续占用 CPU。
// 由于检测/列表响应**绝不下发图片字节**（判据③），预览字节只能来自独立的
// 管理端附件接口，因此控制器只管理「播放状态与帧序号」，不碰字节。
//
// 帧推进按固定帧时长累加：浏览器对 `<img>` 里的 GIF 播放进度不暴露 API，
// 所以这里用「重新赋值 src 触发重启 + 用 CSS/时间戳估算当前帧」之外的第三条路——
// 由调用方按帧时长给 tick(deltaMs)，控制器负责取模回绕。
export function createGifPlaybackController(options = {}) {
  const frameCount = readPositiveInt(options.frameCount) ?? 1;
  const frameDurationMs = readPositiveInt(options.frameDurationMs) ?? 100;
  let playing = options.playing !== false;
  let frameIndex = 0;
  let elapsedMs = 0;

  const clampFrame = (value) => {
    const number = Number(value);
    if (!Number.isFinite(number)) return 0;
    const floored = Math.floor(number);
    const wrapped = floored % frameCount;
    return wrapped < 0 ? wrapped + frameCount : wrapped;
  };

  return {
    // play / pause / toggle 只改播放态，不动帧序号：暂停后再播放应接着看，而不是跳回第一帧。
    play() {
      playing = true;
      return playing;
    },
    pause() {
      playing = false;
      return playing;
    },
    toggle() {
      playing = !playing;
      return playing;
    },
    // restart 是「从头看」：帧序号与累计时长一起归零，并回到播放态。
    restart() {
      frameIndex = 0;
      elapsedMs = 0;
      playing = true;
      return frameIndex;
    },
    // selectFrame 显式跳帧（暂停态下逐帧核对用）。
    selectFrame(index) {
      frameIndex = clampFrame(index);
      elapsedMs = 0;
      return frameIndex;
    },
    // tick 推进时间。暂停时**消费掉** deltaMs 但不推进帧，
    // 避免暂停期间累积的时间在恢复播放的瞬间一次性跳很多帧。
    tick(deltaMs) {
      const delta = Number(deltaMs);
      if (!Number.isFinite(delta) || delta <= 0) return frameIndex;
      if (!playing) {
        elapsedMs = 0;
        return frameIndex;
      }
      elapsedMs += delta;
      while (elapsedMs >= frameDurationMs) {
        elapsedMs -= frameDurationMs;
        frameIndex = (frameIndex + 1) % frameCount;
      }
      return frameIndex;
    },
    get playing() {
      return playing;
    },
    get frameIndex() {
      return frameIndex;
    },
    get frameCount() {
      return frameCount;
    },
    get frameDurationMs() {
      return frameDurationMs;
    },
    get elapsedMs() {
      return elapsedMs;
    },
    // frameLabel 是给 UI 直接渲染的「3 / 12」文本。
    get frameLabel() {
      return `${frameIndex + 1} / ${frameCount}`;
    },
  };
}

// shouldOfferGifControls 判断某条记录/附件是否该显示播放控制条。
//
// 只有动图才需要：静态 PNG/JPEG 的「播放/暂停」是无意义按钮。
// 后端当前只接受 png/jpeg（service/ticket_attachment_image.go），所以本函数
// 对现有数据恒为 false —— 这是事实，不是缺陷；格式扩展后自动生效。
export function shouldOfferGifControls(contentType) {
  const type = String(contentType ?? '').trim().toLowerCase();
  if (!type) return false;
  return type === 'image/gif' || type === 'image/webp' || type === 'image/apng';
}

// readGifFrameCount / readGifFrameDuration 从一条检测记录里读动图播放参数。
//
// 抽成导出的纯函数而不是写在页面里，理由与其它 helper 相同：写在组件内部
// 只能靠「源码里有没有这串字符串」来断言，而那种断言在有人把整行换成常量时
// 仍然是绿的（源码里的 `readPositiveInt(record?.frameCount)` 一字不动地留着）。
// 抽出来之后，测试可以直接喂 { frameCount: 12 } 断言拿到 12。
//
// 为什么必须从记录读而不是写死：后端当前不下发帧信息（见 shouldOfferGifControls），
// 未来一旦下发，写死 1 会让所有动图都只剩一帧、且没有任何报错。
export function readGifFrameCount(record) {
  return readPositiveInt(record?.frameCount) ?? 1;
}

export function readGifFrameDuration(record) {
  return readPositiveInt(record?.frameDurationMs) ?? 100;
}

// buildGifControllerOptions 把一条记录收敛成控制器构造参数。
//
// frameCountKnown 为 false 时页面必须如实说明「帧数未知」——
// 显示「1 / 1」而不解释会让管理员以为动图只有一帧。
export function buildGifControllerOptions(record) {
  const frameCount = readGifFrameCount(record);
  return {
    frameCount,
    frameDurationMs: readGifFrameDuration(record),
    playing: true,
    frameCountKnown: frameCount > 1,
  };
}

// ---- 额度读数 ----

// summarizeQuota 把额度接口响应收敛成展示用的四元组。
//
// remaining 只在后端给了才算：dailyUserLimit 为 0（未配置上限）时后端不加这个字段，
// 前端不能自己用 0 - used 拼一个负数出来。
export function summarizeQuota(payload) {
  const body = payload && typeof payload === 'object' ? payload : {};
  const enabled = body.enabled === true;
  const perTicketLimit = readNonnegativeIntOrNull(body.perTicketLimit);
  const dailyUserLimit = readNonnegativeIntOrNull(body.dailyUserLimit);
  const usedCount = readNonnegativeIntOrNull(body.usedCount);
  const remaining = readNonnegativeIntOrNull(body.remaining);
  const userId = readNonnegativeIntOrNull(body.userId);
  const date = typeof body.date === 'string' ? body.date : '';
  return { enabled, perTicketLimit, dailyUserLimit, usedCount, remaining, userId, date };
}
