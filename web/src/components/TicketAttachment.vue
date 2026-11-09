<template>
  <div class="ticket-attachment" :class="{ 'is-hidden': !visible, 'is-loading': busy }">
    <!-- 敏感 / 未检测 / 检测失败：只渲染占位块，不发起任何图片请求。 -->
    <div v-if="!visible" class="ticket-attachment-placeholder" role="img" :aria-label="placeholderText">
      <svg class="ticket-attachment-icon" viewBox="0 0 64 64" aria-hidden="true" focusable="false">
        <!-- 等价语义：一张带折角的图片边框 + 山峦轮廓 + 右侧禁止标记。 -->
        <path
          d="M10 12h32l12 12v28a4 4 0 0 1-4 4H10a4 4 0 0 1-4-4V16a4 4 0 0 1 4-4Z"
          fill="none"
          stroke="currentColor"
          stroke-width="3"
          stroke-linejoin="round"
        />
        <path d="M42 12v12h12" fill="none" stroke="currentColor" stroke-width="3" stroke-linejoin="round" />
        <path
          d="M12 46l12-14 9 11 6-7 7 10"
          fill="none"
          stroke="currentColor"
          stroke-width="3"
          stroke-linejoin="round"
        />
        <circle cx="46" cy="42" r="13" fill="none" stroke="currentColor" stroke-width="3" />
        <path d="M37 51 55 33" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
      </svg>
      <p class="ticket-attachment-hint">{{ placeholderText }}</p>
      <!-- 用户端没有任何查看入口；管理端才出现按钮。 -->
      <a-button
        v-if="canReveal"
        size="small"
        :loading="busy"
        class="ticket-attachment-reveal"
        :onClick="reveal"
        >显示图片</a-button
      >
    </div>
    <!-- 已通过检测（或被管理员显式放行）：渲染真实图片。 -->
    <figure v-else class="ticket-attachment-figure">
      <img
        :src="objectUrl"
        :alt="altText"
        referrerpolicy="no-referrer"
        loading="lazy"
        class="ticket-attachment-image"
      />
      <figcaption v-if="wasRevealed" class="ticket-attachment-caption">
        该图片被判定为敏感内容，仅供管理员核查
      </figcaption>
    </figure>
    <p v-if="errorText" class="ticket-attachment-error" role="alert">{{ errorText }}</p>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from "vue";
import {
  getTicketAttachment,
  getTicketAttachmentAsAdmin,
} from "@/api/TicketApi";
import {
  isTicketAttachmentVisible,
  ticketAttachmentPlaceholder,
} from "@/utils/ticketAttachmentHelpers.mjs";

defineOptions({ name: "TicketAttachment" });

const props = defineProps({
  attachment: { type: Object, default: null },
  // 只有管理端会传 true：多一个「显示图片」按钮，点击后走带审计的管理端接口。
  admin: { type: Boolean, default: false },
  // 自动加载：仅在 visible=true（服务端已放行）时才拉取，省掉一次 403。
  autoLoad: { type: Boolean, default: true },
});

const objectUrl = ref("");
const busy = ref(false);
const errorText = ref("");
const revealed = ref(false);
let loadVersion = 0;
let active = true;

const visible = computed(() => isTicketAttachmentVisible(props.attachment));
const placeholderText = computed(() => ticketAttachmentPlaceholder(props.attachment));
// 敏感图只有管理员能点「显示图片」按钮；用户端**没有**任何按钮。
const canReveal = computed(() => props.admin && !visible.value);
const wasRevealed = computed(() => revealed.value && visible.value === false);
const altText = computed(() => {
  const id = props.attachment?.id;
  return id ? `工单图片 #${id}` : "工单图片";
});

function revoke() {
  if (objectUrl.value) {
    URL.revokeObjectURL(objectUrl.value);
    objectUrl.value = "";
  }
}

// fetchAttachment 只有两条入口：
//   - autoLoad：服务端已判定 visible=true；
//   - 管理员点「显示图片」：走 admin 接口，服务端写审计。
// 任何情况下都不会在 visible=false 且非管理员时发起请求。
async function fetchAttachment(useAdminEndpoint) {
  const id = Number(props.attachment?.id);
  if (!Number.isInteger(id) || id <= 0) return;
  const version = ++loadVersion;
  busy.value = true;
  errorText.value = "";
  try {
    const response = useAdminEndpoint
      ? await getTicketAttachmentAsAdmin(id)
      : await getTicketAttachment(id);
    if (!active || version !== loadVersion) return;
    const blob = response?.data;
    if (!(blob instanceof Blob) || !/^image\/(png|jpeg)$/.test(blob.type)) {
      errorText.value = "未能读取该图片";
      return;
    }
    revoke();
    objectUrl.value = URL.createObjectURL(blob);
  } catch (error) {
    if (!active || version !== loadVersion) return;
    // 403（敏感被拦下）也是正常路径：后端可能刚好把状态更新为 sensitive。
    errorText.value = useAdminEndpoint
      ? "图片读取失败，请稍后重试"
      : String(error?.message || "").trim() || "图片读取失败";
  } finally {
    if (active && version === loadVersion) busy.value = false;
  }
}

function reveal() {
  if (!canReveal.value || busy.value) return;
  revealed.value = true;
  fetchAttachment(true);
}

watch(
  () => [props.attachment?.id, visible.value],
  () => {
    revoke();
    errorText.value = "";
    revealed.value = false;
    if (props.autoLoad && visible.value) fetchAttachment(false);
  },
  { immediate: true }
);

onBeforeUnmount(() => {
  active = false;
  loadVersion += 1;
  revoke();
});
</script>

<style scoped>
.ticket-attachment {
  max-width: 260px;
}
.ticket-attachment-placeholder {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 14px 12px;
  border: 1px dashed var(--console-border);
  border-radius: 10px;
  background: var(--console-subtle);
  color: var(--console-muted);
  text-align: center;
}
.ticket-attachment-icon {
  width: 44px;
  height: 44px;
  color: var(--console-muted);
  opacity: 0.75;
}
.ticket-attachment-hint {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  overflow-wrap: anywhere;
}
.ticket-attachment-figure {
  margin: 0;
}
.ticket-attachment-image {
  display: block;
  max-width: 100%;
  border: 1px solid var(--console-border);
  border-radius: 10px;
}
.ticket-attachment-caption {
  margin-top: 6px;
  color: var(--console-muted);
  font-size: 12px;
  line-height: 1.6;
}
.ticket-attachment-error {
  margin: 6px 0 0;
  color: #cf1322;
  font-size: 12px;
}
.ticket-attachment.is-loading {
  opacity: 0.85;
}
</style>
