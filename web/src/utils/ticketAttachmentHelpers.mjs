// 工单图片附件的纯函数：状态文案、展示判定、文件预检。
//
// 这些逻辑**必须**与后端 service.ticketAttachmentVisible 保持一致：
// 后端只有 scan_state === 'approved' 才下发字节，前端不得自行放宽，
// 也不得在 visible=false 时尝试拉取图片（那只会换来一个 403）。

export const TICKET_ATTACHMENT_ACCEPT = "image/png,image/jpeg";
export const TICKET_ATTACHMENT_MAX_BYTES = 2 * 1024 * 1024;
export const TICKET_ATTACHMENT_MAX_PER_MESSAGE = 6;

export const TICKET_ATTACHMENT_HINTS = Object.freeze({
  sensitive: "此图片包含敏感内容，已隐藏",
  pending: "图片正在进行安全检测，暂不显示",
  failed: "图片未通过安全检测，已隐藏",
});

// ticketAttachmentPlaceholder 返回占位块要显示的文案。
// 后端已算好 visible / hiddenReason，前端优先用后端字段（单一口径），
// 只有后端字段缺失时才按 scanState 本地兜底。
export function ticketAttachmentPlaceholder(attachment) {
  if (!attachment || attachment.visible) return "";
  const reason = String(attachment.hiddenReason || "").trim();
  if (reason) return reason;
  const state = String(attachment.scanState || "").trim().toLowerCase();
  if (state === "sensitive") return TICKET_ATTACHMENT_HINTS.sensitive;
  if (state === "pending" || state === "scanning") return TICKET_ATTACHMENT_HINTS.pending;
  return TICKET_ATTACHMENT_HINTS.failed;
}

// isTicketAttachmentVisible 是前端唯一的展示判据。
// 注意：它只决定「是否渲染占位」，真正能否拿到字节仍由服务端裁决。
export function isTicketAttachmentVisible(attachment) {
  return Boolean(attachment && attachment.visible === true);
}

// validateTicketAttachmentFile 在浏览器侧做与后端同构的预检，减少无用的网络往返。
// 注意：扩展名/类型只是**提示**，真正的格式判定在服务端（读真实魔数）。
export function validateTicketAttachmentFile(file, limit = TICKET_ATTACHMENT_MAX_PER_MESSAGE) {
  if (!file) return "请选择图片文件";
  const type = String(file.type || "").toLowerCase();
  const name = String(file.name || "").toLowerCase();
  const acceptedType = type === "image/png" || type === "image/jpeg";
  const acceptedName = name.endsWith(".png") || name.endsWith(".jpg") || name.endsWith(".jpeg");
  if (!acceptedType && !acceptedName) return "仅支持 PNG / JPEG 图片";
  const size = Number(file.size);
  if (!Number.isFinite(size) || size <= 0) return "图片内容为空";
  if (size > TICKET_ATTACHMENT_MAX_BYTES) return "图片不能超过 2 MiB";
  if (Number(limit) <= 0) return ""; // 计数由调用方按剩余额度判定
  return "";
}

// pickTicketAttachmentIds 把上传结果收敛成 ID 列表，供建单/回复时提交。
export function pickTicketAttachmentIds(attachments) {
  if (!Array.isArray(attachments)) return [];
  return attachments
    .map((item) => Number(item?.id))
    .filter((id) => Number.isInteger(id) && id > 0);
}

// groupTicketAttachmentsByMessage 把附件按 messageId 分组，供消息流渲染。
// messageId 为空的条目表示「挂在工单上」而还没对应消息（例如旧数据或上传后未绑定）。
export function groupTicketAttachmentsByMessage(attachments) {
  const groups = new Map();
  if (!Array.isArray(attachments)) return groups;
  for (const item of attachments) {
    const key = item?.messageId == null ? "ticket" : String(item.messageId);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(item);
  }
  return groups;
}
