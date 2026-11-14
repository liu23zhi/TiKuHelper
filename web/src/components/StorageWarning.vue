<template>
  <div v-if="visible" class="storage-warning" role="status" aria-live="polite" data-role="storage-warning">
    <span class="storage-warning__badge" aria-hidden="true">SQLite</span>
    <p class="storage-warning__text">{{ warning }}</p>
    <button type="button" class="storage-warning__close" data-action="dismiss-storage-warning"
            aria-label="本次登录内关闭存储警告" @click="dismiss">关闭</button>
  </div>
</template>

<script>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  STORAGE_WARNING_DISMISS_KEY,
  buildStorageWarningAnnouncement,
  resolveStorageWarningVisibility,
} from '../utils/storageWarning.mjs'

/**
 * SQLite 悬浮警告（task_doc/108:297、109:474/486；Q7-a 按「WARN + 前端悬浮」实现）。
 *
 * # 为什么是「悬浮」而不是页面内嵌
 *
 * 用户原话是「使用 Sqlite 应该显示的**悬浮**警告」。内嵌条会随页面滚动走掉 ——
 * 而这条警告的性质是「整个实例处于不被推荐的状态」，它必须一直可见，
 * 只有用户主动关掉才消失。所以 position: fixed + 顶部居中。
 *
 * # 关闭的语义
 *
 * 关闭只作用于**本次登录**：把关闭时的会话代次写进 localStorage。
 * 重新登录会推进代次（见 utils/currentUser.js），于是记录自动失效、警告重现。
 * 不这么做就会出现「一次误关 = 永久消失」，等于这条警告形同虚设。
 *
 * # 文案来源
 *
 * 一个字的文案都不在前端写死（除了「关闭」按钮与 aria-label）。
 * 展示内容完全来自后端 `/api/system/storage-status` 的 warning 字段，
 * 与启动日志同源 —— 否则「日志说 A、界面说 B」的漂移必然发生。
 */
export default {
  name: 'StorageWarning',
  props: {
    // 后端响应体：{ driver, warning, production }。null 表示还没取到/取失败。
    status: { type: Object, default: null },
    // 当前会话代次（由布局传入，便于测试注入）。
    sessionRevision: { type: String, default: '' },
    // 存储读写注入点：默认用 window.localStorage，测试可替换。
    storage: { type: Object, default: null },
  },
  emits: ['dismiss'],
  setup(props, { emit }) {
    // 组件内「刚刚关掉」的本地标记：让关闭立即生效，不必等 storage 事件。
    const localDismissed = ref('')

    const store = () => props.storage || (typeof window !== 'undefined' ? window.localStorage : null)

    function readDismissed() {
      // 读不到（隐私模式 / 存储被禁）一律当作「没关过」：
      // 宁可多显示一次，也不能让读取失败变成永久静默。
      const target = store()
      if (!target || typeof target.getItem !== 'function') return ''
      try {
        return String(target.getItem(STORAGE_WARNING_DISMISS_KEY) || '')
      } catch {
        return ''
      }
    }

    const dismissedRevision = ref(readDismissed())

    const warning = computed(() => {
      const value = props.status && typeof props.status.warning === 'string' ? props.status.warning.trim() : ''
      return value
    })

    const visible = computed(() => {
      if (!resolveStorageWarningVisibility(props.status, dismissedRevision.value, props.sessionRevision)) return false
      // 本地刚关过且代次未变 —— storage 读不到时也能立即生效。
      return !(localDismissed.value && localDismissed.value === props.sessionRevision)
    })

    const announcement = computed(() => buildStorageWarningAnnouncement(props.status))

    function dismiss() {
      const revision = props.sessionRevision || ''
      localDismissed.value = revision
      dismissedRevision.value = revision
      const target = store()
      if (target && typeof target.setItem === 'function') {
        try {
          if (revision) target.setItem(STORAGE_WARNING_DISMISS_KEY, revision)
          // 代次为空时**不写**：写空串等于记下一条「空关记录」，
          // 而 shouldRespectDismiss 会把空记录判成「没关过」，语义虽对但脏数据无益。
        } catch { /* 写失败不影响本次关闭（localDismissed 已生效） */ }
      }
      emit('dismiss', revision)
    }

    // 代次变化（重新登录）→ 丢掉本地标记并重读存储。
    // 不重读的话，同一标签页内重新登录后 visible 会停留在关闭态。
    watch(() => props.sessionRevision, () => {
      localDismissed.value = ''
      dismissedRevision.value = readDismissed()
    })

    // 多标签页：另一个标签关掉后这里也收起（同代次才同步）。
    let host = null
    function onStorage(event) {
      if (!event || event.key !== STORAGE_WARNING_DISMISS_KEY) return
      dismissedRevision.value = String(event.newValue || '')
    }
    onMounted(() => {
      host = typeof window !== 'undefined' ? window : null
      if (host && typeof host.addEventListener === 'function') host.addEventListener('storage', onStorage)
    })
    onBeforeUnmount(() => {
      if (host && typeof host.removeEventListener === 'function') host.removeEventListener('storage', onStorage)
      host = null
    })

    return { visible, warning, announcement, dismiss }
  },
}
</script>

<style scoped>
/*
 * 悬浮定位。
 *
 * top 用 calc 而不是固定值：跳过链接（.skip-link）在 top: var(--skip-link-inset)，
 * 页头是 sticky 的。取一个比页头略低的偏移，避免压住品牌名与主题切换按钮 ——
 * 那两处是每页都要用的主操作。
 *
 * z-index 1100：与 .skip-link 同档。介于「内容」(auto) 与 antd Drawer/Modal(1000+)
 * 之上但不超过 antd 的 message(1010)/tooltip(1070) 体系之外的弹层，
 * 保证它浮在内容之上、又不会被抽屉遮住时显得错位。
 */
.storage-warning {
  position: fixed;
  top: calc(var(--skip-link-inset, 8px) + 44px);
  left: 50%;
  transform: translateX(-50%);
  z-index: 1100;
  display: flex;
  align-items: center;
  gap: 10px;
  max-width: min(92vw, 720px);
  padding: 10px 14px;
  border: 1px solid var(--warning-border);
  border-radius: var(--radius-control, 12px);
  background: var(--warning-bg);
  color: var(--warning-fg);
  /*
   * 阴影用主题变量而不是 rgba(0,0,0,.16)：写死黑色阴影在深色主题下
   * 几乎不可见（黑底上叠黑色），深浅两套主题的阴影强度本来就不同。
   * --shadow-color 是本仓既有的跨主题阴影色令牌，两套主题都无条件定义
   * （theme-tokens.css:42 浅色 / :103 深色），所以这里不写兜底值。
   */
  box-shadow: 0 8px 24px var(--shadow-color);
}

.storage-warning__badge {
  flex: 0 0 auto;
  padding: 2px 8px;
  border-radius: var(--radius-chip, 8px);
  background: var(--warning-border);
  color: var(--warning-bg);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.storage-warning__text {
  margin: 0;
  font-size: 13px;
  line-height: 1.5;
}

.storage-warning__close {
  flex: 0 0 auto;
  padding: 4px 10px;
  border: 1px solid currentColor;
  border-radius: var(--radius-chip, 8px);
  background: transparent;
  color: inherit;
  font-size: 12px;
  cursor: pointer;
}

.storage-warning__close:hover,
.storage-warning__close:focus-visible {
  background: var(--warning-border);
  color: var(--warning-bg);
}

/*
 * 移动端：不遮挡主操作。
 *
 * 手机上「导航」按钮与页头在顶部，悬浮条压在最上面会挡住它们，
 * 所以改成贴底并留出安全区。贴底还有一个好处：手机用户的拇指在下方，
 * 「关闭」按钮更容易点到。
 */
@media (max-width: 767px) {
  .storage-warning {
    top: auto;
    bottom: calc(12px + env(safe-area-inset-bottom, 0px));
    left: 12px;
    right: 12px;
    transform: none;
    max-width: none;
    align-items: flex-start;
  }
}

/*
 * 深色模式：只依赖 theme-tokens.css 的 --warning-* 三件套。
 *
 * 这三组变量在两套主题里都定义了（浅色 #854D0E on #FFF5DA、深色 #F4CF83 on #3A2E17），
 * 对比度都达标，所以这里不写任何颜色字面量 —— 写死颜色会让深色模式变成
 * 「浅底深字 + 深色页头」的错位块。
 */
@media (prefers-reduced-motion: reduce) {
  .storage-warning { transition: none; }
}
</style>
