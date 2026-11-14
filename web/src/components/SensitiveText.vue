<template>
  <span class="sensitive-text" :class="{ 'sensitive-text--masked': isMasked }">
    <span v-if="isSensitive" class="sensitive-text__badge" :title="badge.title">{{ badge.label }}</span>
    <span class="sensitive-text__value">{{ displayText }}</span>
    <button
      v-if="revealable"
      type="button"
      class="sensitive-text__toggle"
      :aria-pressed="expanded ? 'true' : 'false'"
      :disabled="loading"
      @click="onToggle"
    >{{ toggleLabel }}</button>
  </span>
</template>

<script>
import { computed } from 'vue';
import {
  buildLocalMaskPlaceholder,
  buildSensitiveBadgeState,
} from '@/utils/sensitiveDisplay.mjs';

/**
 * 敏感文本的统一展示组件：**默认为遮挡**，管理员点按钮才展开原文。
 *
 * 用户原话：「所有页面都默认打标，只是管理员有按钮可以显示原文」。
 *
 * ## 契约（与数据源解耦）
 *
 * 本组件**不发任何请求**，只消费 props、只 emit 事件。取原文由调用方负责
 * （管理员接口 `GET /api/logs/:id/sensitive-original` 挂 RequireAdmin）。
 * 这样 TIKU 的日志页、工单页与 PLUS 的 chaoxing 面板可以共用同一份展示规则，
 * 而各自的取数方式互不牵制。
 *
 * props:
 * - `text`        当前要显示的文本（可能已被服务端遮挡）
 * - `maskText`    显式遮挡占位；缺省按 `text` 的码点长度本地生成
 * - `isSensitive` 该条是否命中敏感词（后端 `sensitiveFlag === true`）
 * - `serverMasked` 后端是否已遮挡（`sensitiveMasked === true`）
 * - `canReveal`   是否允许展开（管理员）；普通用户恒 false
 * - `revealed`    受控展开状态（配合 `update:revealed` 使用）
 * - `loading`     原文请求进行中
 *
 * emits: `update:revealed` / `reveal` / `hide`
 *
 * ## 安全默认（三条都不依赖调用方记得做什么）
 *
 * 1. 未传 `revealed` 时恒不展开 —— 不存在「忘了初始化就泄露原文」。
 * 2. `revealed=true` 但 `canReveal=false` 时**强制按遮挡渲染** ——
 *    即使父组件状态写错，非管理员也拿不到原文。
 * 3. `isSensitive` 为真但服务端没遮挡（老后端）时本地按长度生成占位，
 *    绝不因为「这次没拿到遮挡文本」就把原文显示出来。
 */
const SensitiveText = {
  name: 'SensitiveText',
  props: {
    text: { type: [String, Number], default: '' },
    maskText: { type: String, default: '' },
    isSensitive: { type: Boolean, default: false },
    serverMasked: { type: Boolean, default: false },
    canReveal: { type: Boolean, default: false },
    revealed: { type: Boolean, default: false },
    loading: { type: Boolean, default: false },
  },
  emits: ['update:revealed', 'reveal', 'hide'],
  setup(props, { emit }) {
    const rawText = computed(() => (props.text === '' || props.text == null ? '' : String(props.text)));

    // 判据只有「后端说命中了」这一条：不从文本内容反推敏感词。
    const isSensitive = computed(() => props.isSensitive === true);

    // 只有管理员才有展开权；reaveled 单独为真不生效。
    const expanded = computed(() => props.revealed === true && props.canReveal === true);

    const isMasked = computed(() => isSensitive.value && !expanded.value);

    const displayText = computed(() => {
      if (!isSensitive.value || expanded.value) {
        return rawText.value;
      }
      // 服务端已遮挡：直接用它下发的遮挡文本（不要重复遮，那是幂等的但没必要）。
      if (props.serverMasked === true) {
        return rawText.value === '' ? buildLocalMaskPlaceholder('') : rawText.value;
      }
      // 老后端 / 调用方没走遮挡：本地兜底遮挡，只报长度。
      return props.maskText || buildLocalMaskPlaceholder(rawText.value);
    });

    const revealable = computed(() => props.canReveal === true && isSensitive.value);

    const toggleLabel = computed(() => (expanded.value ? '隐藏原文' : '显示原文'));

    const badge = computed(() => buildSensitiveBadgeState(
      { sensitiveFlag: isSensitive.value },
      { revealed: expanded.value, isAdmin: props.canReveal === true },
    ));

    function onToggle() {
      const next = !expanded.value;
      emit('update:revealed', next);
      emit(next ? 'reveal' : 'hide');
    }

    return { isSensitive, isMasked, expanded, displayText, revealable, toggleLabel, badge, onToggle };
  },
};

export default SensitiveText;
</script>

<style scoped>
.sensitive-text {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
  max-width: 100%;
}

.sensitive-text__value {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  min-width: 0;
}

/* 遮挡态：弱化 + 等宽数字，让「这里被挡了」一眼可辨，而不是像正文。 */
.sensitive-text--masked .sensitive-text__value {
  color: var(--console-muted, #8c8c8c);
  font-variant-numeric: tabular-nums;
}

.sensitive-text__badge {
  flex: none;
  padding: 0 6px;
  border: 1px solid currentColor;
  border-radius: 4px;
  color: var(--console-danger, #c13e41);
  font-size: 12px;
  line-height: 18px;
}

.sensitive-text__toggle {
  flex: none;
  padding: 0;
  border: 0;
  background: none;
  color: var(--console-link, #1677ff);
  font: inherit;
  cursor: pointer;
  text-decoration: underline;
}

.sensitive-text__toggle:disabled {
  color: var(--console-muted, #8c8c8c);
  cursor: progress;
  text-decoration: none;
}
</style>
