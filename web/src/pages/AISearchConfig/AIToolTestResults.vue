<template>
  <ol v-if="calls.length" class="tool-call-list" aria-label="实际工具调用">
    <li v-for="(call, index) in calls" :key="call.key" class="tool-call">
      <div class="tool-call-heading">
        <h4><span class="tool-call-index">{{ index + 1 }}</span>{{ call.title }} <span class="tool-call-name">{{ call.tool }}</span></h4>
        <a-tag :color="call.statusColor">{{ call.statusText }}</a-tag>
      </div>
      <dl class="tool-call-target">
        <dt>{{ call.targetLabel }}</dt>
        <dd>
          <a v-if="call.targetHref" :href="call.targetHref" target="_blank" rel="noopener noreferrer">{{ call.target }}</a>
          <span v-else>{{ call.target || '-' }}</span>
        </dd>
      </dl>
      <p v-if="call.error" class="tool-call-error">{{ call.error }}</p>
      <template v-if="call.tool === 'web_search'">
        <p class="tool-call-meta">
          工具返回 {{ call.resultCount }} 条结果<template v-if="call.totalResults !== null">，上游报告共 {{ call.totalResults }} 条</template>
        </p>
        <ul v-if="call.sources.length" class="search-sources" aria-label="搜索来源">
          <li v-for="(source, sourceIndex) in call.sources" :key="sourceIndex">
            {{ source.name || '未命名来源' }}<template v-if="source.resultCount !== null"> · {{ source.resultCount }} 条</template>
          </li>
        </ul>
        <ol v-if="call.results.length" class="search-results" tabindex="0" aria-label="搜索返回结果">
          <li v-for="(item, resultIndex) in call.results" :key="resultIndex" class="search-result">
            <a v-if="item.href" :href="item.href" target="_blank" rel="noopener noreferrer" class="search-result-title">{{ item.title || item.url || '未命名结果' }}</a>
            <span v-else class="search-result-title">{{ item.title || '未命名结果' }}</span>
            <p v-if="item.url" class="search-result-url">{{ item.url }}</p>
            <p v-if="item.domain || item.source" class="tool-call-meta">{{ [item.domain, item.source].filter(Boolean).join(' · ') }}</p>
            <p v-if="item.snippet" class="search-result-snippet">{{ item.snippet }}</p>
          </li>
        </ol>
        <p v-else class="tool-call-hint">未返回可展示的搜索结果。</p>
      </template>
      <template v-else-if="call.tool === 'image_ocr'">
        <p class="tool-call-meta">缓存状态：{{ call.cached === true ? '缓存命中' : call.cached === false ? '未命中缓存' : '未知' }}</p>
        <template v-if="call.text">
          <h5>实际识别文本</h5>
          <pre class="web-read-content" tabindex="0" aria-label="OCR 识别文本">{{ call.text }}</pre>
        </template>
        <p v-else class="tool-call-hint">未返回识别文本。</p>
      </template>
      <template v-else>
        <div v-if="call.taskId || call.taskStatus" class="web-task-meta">
          <span v-if="call.taskId">任务编号：{{ call.taskId }}</span>
          <span v-if="call.taskStatus">任务状态：{{ call.taskStatus }}</span>
        </div>
        <p v-if="call.taskMessage" class="tool-call-meta">上游说明：{{ call.taskMessage }}</p>
        <template v-if="call.markdown">
          <h5>工具返回的网页正文</h5>
          <pre class="web-read-content" tabindex="0" aria-label="网页返回正文">{{ call.markdown }}</pre>
        </template>
        <p v-else class="tool-call-hint">尚未返回网页正文。</p>
      </template>
    </li>
  </ol>
  <p v-else class="tool-call-hint">未记录到实际工具调用，可展开下方调用轨迹查看原因。</p>
</template>

<script>
export default {
  name: 'AIToolTestResults',
  props: { calls: { type: Array, default: () => [] } },
}
</script>

<style scoped>
.tool-call-list { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; margin: 0; padding: 0; list-style: none; }
.tool-call { min-width: 0; padding: 16px; border: 1px solid var(--console-border); border-radius: 8px; background: var(--console-surface); overflow-wrap: anywhere; }
.tool-call-heading { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 12px; }
.tool-call-heading h4 { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; min-width: 0; margin: 0; font-size: 14px; color: var(--console-text); }
.tool-call-heading :deep(.ant-tag) { margin: 0; }
.tool-call-index { display: inline-flex; align-items: center; justify-content: center; min-width: 24px; height: 24px; padding-inline: 4px; border-radius: 4px; background: var(--console-selected); color: var(--console-primary); }
.tool-call-name, .tool-call-meta, .tool-call-hint, .search-result-url, .web-task-meta { color: var(--console-muted); font-size: 12px; }
.tool-call-name { font-weight: 400; }
.tool-call-target { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; margin: 0 0 12px; }
.tool-call-target dt { color: var(--console-muted); font-size: 12px; }
.tool-call-target dd { min-width: 0; margin: 0; white-space: pre-wrap; line-height: 1.7; }
.tool-call a { color: var(--console-primary); }
.tool-call-error { padding: 8px 12px; border-inline-start: 3px solid var(--console-danger); background: var(--console-subtle); color: var(--console-danger); white-space: pre-wrap; }
.tool-call-meta, .tool-call-hint { margin: 8px 0 0; line-height: 1.7; white-space: pre-wrap; }
.search-sources { display: flex; flex-wrap: wrap; gap: 4px 12px; padding: 0; margin: 8px 0 0; list-style: none; color: var(--console-muted); font-size: 12px; }
.search-results { max-height: 360px; overflow: auto; margin: 12px 0 0; padding-inline-start: 24px; }
.search-result { min-width: 0; padding: 12px 0; border-top: 1px solid var(--console-border); }
.search-result:first-child { padding-top: 0; border-top: 0; }
.search-result-title { font-weight: 600; line-height: 1.7; }
.search-result-url { margin: 4px 0 0; line-height: 1.7; }
.search-result-snippet { margin: 8px 0 0; white-space: pre-wrap; line-height: 1.7; color: var(--console-text); }
.web-task-meta { display: flex; flex-wrap: wrap; gap: 4px 16px; line-height: 1.7; }
.tool-call h5 { margin: 12px 0 8px; font-size: 13px; font-weight: 600; color: var(--console-text); }
.web-read-content { max-width: 100%; max-height: 360px; overflow: auto; margin: 0; padding: 12px; background: var(--console-subtle); border-radius: 4px; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 12px; line-height: 1.7; color: var(--console-text); }
@media (max-width: 480px) {
  .tool-call { padding: 12px; }
  .search-results { padding-inline-start: 20px; }
}
</style>
