<template>
  <div class="overflow-table-text">
    <div
      ref="content"
      :class="['overflow-table-text__content', { 'overflow-table-text__content--expanded': expanded }]"
      :style="{ '--preview-lines': lines }"
    >{{ text || '-' }}</div>
    <a-button v-if="expanded || overflowing" type="link" size="small" class="overflow-table-text__toggle" @click="$emit('toggle')">
      {{ expanded ? '收起' : '查看详情' }}
    </a-button>
  </div>
</template>

<script>
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';

export default {
  name: 'OverflowTableText',
  props: {
    text: { type: [String, Number], default: '' },
    expanded: { type: Boolean, default: false },
    lines: { type: Number, default: 3 },
  },
  emits: ['toggle'],
  setup(props) {
    const content = ref(null);
    const overflowing = ref(false);
    let observer;

    function measureOverflow() {
      const element = content.value;
      if (element && !props.expanded) {
        overflowing.value = element.scrollHeight > element.clientHeight + 1;
      }
    }

    onMounted(() => {
      nextTick(measureOverflow);
      if (typeof ResizeObserver !== 'undefined') {
        observer = new ResizeObserver(measureOverflow);
        observer.observe(content.value);
      }
    });
    onBeforeUnmount(() => observer?.disconnect());
    watch(() => [props.text, props.expanded, props.lines], async () => {
      await nextTick();
      measureOverflow();
    });
    return { content, overflowing };
  },
};
</script>

<style scoped>
.overflow-table-text {
  min-width: 0;
  width: 100%;
  text-align: center;
}

.overflow-table-text__content {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: var(--preview-lines);
  overflow: hidden;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
  line-height: 1.5;
}

.overflow-table-text__content--expanded {
  display: block;
  -webkit-line-clamp: unset;
  overflow: visible;
}

.overflow-table-text__toggle {
  height: auto;
  padding: 0;
  line-height: 1.4;
  white-space: normal;
}
</style>
