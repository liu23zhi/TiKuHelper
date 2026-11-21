<template>
  <div class="ticket-attachment" :class="{ 'is-hidden': !showImage, 'is-loading': busy }" :aria-busy="busy">
    <!-- 不可见及读取失败统一显示原因；没有可用 URL 时不渲染 img。 -->
    <div v-if="!showImage" class="ticket-attachment-placeholder" role="img" :aria-label="placeholderText">
      <svg class="ticket-attachment-icon" viewBox="0 0 1024 1024" aria-hidden="true" focusable="false">
        <path d="M402.57 905.35H176.1c-61.9-0.27-111.97-50.46-112.1-112.36V231.23c0-61.91 49.98-112.2 111.88-112.58H409.1l34.46 56.41H169.33c-29.14 1.89-51.25 27.03-49.39 56.18v499.93l190.52-230.48c13.82-18.55 40.07-22.38 58.62-8.56 3.25 2.42 6.14 5.31 8.56 8.56l133.62 172.03 27.17 34.87-0.08 0.1 21.6 42.97-157.38 154.69z m445.55 0H544.47L654.99 702.4l-51.58-57.98 92.57-92.99a44.234 44.234 0 0 1 34.86-16.85c1.17 0 2.3 0 3.38 0.13a44.414 44.414 0 0 1 35.25 22.34l134.59 172.03V231.23c-2.13-30.01-25.94-53.92-55.94-56.18h-333.5l-14.59-56.41h348.08c61.91 0.4 111.89 50.69 111.89 112.59v561.75c-0.13 61.82-50.07 111.97-111.88 112.37zM651.72 456.3c-46.67-0.03-84.48-37.88-84.45-84.54 0.03-46.67 37.88-84.48 84.54-84.45 46.67 0.03 84.48 37.88 84.45 84.54a84.509 84.509 0 0 1-24.83 59.79 83.99 83.99 0 0 1-59.69 24.65l-0.02 0.01z" fill="currentColor" />
      </svg>
      <p class="ticket-attachment-hint" :class="{ 'ticket-attachment-error': errorText }" :role="errorText ? 'alert' : undefined">{{ placeholderText }}</p>
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
        @error="onImageError"
      />
      <figcaption v-if="wasRevealed" class="ticket-attachment-caption">
        该图片被判定为敏感内容，仅供管理员核查
      </figcaption>
    </figure>
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
const placeholderText = computed(() => {
  if (errorText.value) return errorText.value;
  if (!visible.value) return ticketAttachmentPlaceholder(props.attachment) || "图片暂不可用";
  return busy.value ? "图片正在加载，请稍候" : "图片暂未加载";
});
// 敏感图只有管理员能点「显示图片」按钮；用户端**没有**任何按钮。
const canReveal = computed(() => props.admin && !visible.value);
const wasRevealed = computed(() => revealed.value && visible.value === false);
const showImage = computed(() => Boolean(objectUrl.value && !errorText.value && (visible.value || (props.admin && revealed.value))));
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

function onImageError() {
  revoke();
  revealed.value = false;
  errorText.value = "图片加载失败，请稍后重试";
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
    revealed.value = useAdminEndpoint;
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
  fetchAttachment(true);
}

watch(
  () => [props.attachment?.id, visible.value, props.admin, props.autoLoad],
  () => {
    loadVersion += 1;
    busy.value = false;
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
