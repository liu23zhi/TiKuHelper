<template>
  <span v-if="canManage" class="user-ban-import">
    <a-upload accept=".csv,.json" :show-upload-list="false" :before-upload="entry.upload" :disabled="busy">
      <a-button :loading="busy" :disabled="busy">导入封禁</a-button>
    </a-upload>
    <a-button type="link" :disabled="busy" :onClick="entry.template">下载导入模板</a-button>
    <span v-if="notice" role="status">{{ notice }}</span>
    <RequestFailureAlert v-if="failure && !visible" :failure="failure" message="封禁预览未完成，请核对文件中的用户 ID、封禁原因和到期时间。" />
    <a-modal :open="visible" title="逐一核对封禁账号" width="1100px" :ok-text="`确认封禁 ${targets.length} 个账号`" cancel-text="取消" :confirm-loading="busy" :ok-button-props="{ danger: true, disabled: !confirmed || !previewId || busy }" :cancel-button-props="{ disabled: busy }" :closable="!busy" :mask-closable="false" :onOk="entry.confirm" :onCancel="entry.cancel">
      <a-alert type="warning" show-icon message="以下账号资料由服务器按用户 ID 查询，文件里的用户名不参与匹配。请核对每个账号、封禁原因和到期时间后确认；取消不会封禁。" />
      <p>支持 CSV / JSON，最多 200 个账号、2 MiB。字段：user_id、reason、expires_at；到期时间留空表示永久，非空使用 RFC3339。</p>
      <RequestFailureAlert v-if="failure" :failure="failure" message="封禁导入未完成，请刷新列表核对后重新预览；本弹窗不会重复提交。" />
      <a-table :data-source="targets" :columns="columns" row-key="userId" size="small" :pagination="{ pageSize: 20, showTotal: total => `共 ${total} 个账号` }" :scroll="{ x: 1500 }">
        <template #bodyCell="{ column, record }">
          <span v-if="column.key === 'role'">{{ record.role === 'admin' ? '管理员' : '用户' }}</span>
          <span v-else-if="column.key === 'status'">{{ statusLabel(record.status) }}</span>
          <span v-else-if="column.key === 'expiresAt'">{{ record.expiresAt ? formatTime(record.expiresAt) : '永久' }}</span>
          <span v-else-if="column.key === 'createdAt'">{{ formatTime(record.createdAt) }}</span>
        </template>
      </a-table>
      <a-checkbox :checked="confirmed" :disabled="busy || !previewId" :onChange="entry.checked">已逐一核对以上 {{ targets.length }} 个账号及封禁期限，确认执行封禁</a-checkbox>
    </a-modal>
  </span>
</template>

<script>
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import RequestFailureAlert from './RequestFailureAlert.vue'
import { getStoredCurrentUser, isAdminUser, useCurrentUserDisplay } from '../utils/currentUser'
import { ensureRequestFailure } from '../utils/requestFailureView.mjs'
import { previewUserBanImport, confirmUserBanImport, deleteUserBanImportPreview } from '../api/UserBanImportApi.mjs'

export default {
  name: 'UserBanImport',
  components: { RequestFailureAlert },
  emits: ['imported'],
  setup(_, { emit }) {
    const { currentUser, captureUpdateGuard } = useCurrentUserDisplay()
    const validAdmin = user => isAdminUser(user) && Number.isSafeInteger(Number(user?.id)) && Number(user.id) > 0 && !['disabled', 'banned', '0'].includes(String(user?.status ?? 'enabled').trim().toLowerCase())
    const canManage = computed(() => validAdmin(currentUser.value))
    const visible = ref(false), busy = ref(false), confirmed = ref(false), previewId = ref(''), targets = ref([]), notice = ref('')
    const failure = shallowRef(null), entry = shallowRef({})
    let alive = true, generation = 0
    const columns = [
      { title: '用户 ID', key: 'userId', dataIndex: 'userId', width: 90 },
      { title: '用户名（当前）', key: 'username', dataIndex: 'username', width: 160 },
      { title: '昵称', key: 'nickname', dataIndex: 'nickname', width: 130 },
      { title: '角色', key: 'role', width: 85 },
      { title: '当前状态', key: 'status', width: 100 },
      { title: '手机号', key: 'phone', dataIndex: 'phone', width: 140 },
      { title: '微信', key: 'wechat', dataIndex: 'wechat', width: 150 },
      { title: '注册时间', key: 'createdAt', width: 170 },
      { title: '封禁原因', key: 'reason', dataIndex: 'reason', width: 230 },
      { title: '到期时间', key: 'expiresAt', width: 170 },
    ]
    const formatTime = value => {
      const date = new Date(value)
      return Number.isFinite(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false }) : '-'
    }
    const statusLabel = value => ({ enabled: '正常', banned: '已封禁', disabled: '停用', closure_pending: '注销申请中' }[value] || value || '-')
    function reset() { visible.value = false; busy.value = false; confirmed.value = false; previewId.value = ''; targets.value = []; notice.value = ''; failure.value = null }
    function makeEntry() {
      const version = generation, isCurrentUser = captureUpdateGuard()
      const owns = () => alive && version === generation && isCurrentUser() && validAdmin(getStoredCurrentUser())
      return {
        upload(file) {
          if (!owns() || busy.value) return false
          if (file.size > 2 * 1024 * 1024) { failure.value = ensureRequestFailure({ code: 'FILE_TOO_LARGE' }); return false }
          reset(); busy.value = true
          void (async () => {
            try {
              const response = await previewUserBanImport(file)
              if (!owns()) return
              const result = response?.data
              if (response?.status !== 200 || !/^[a-zA-Z0-9_-]{8,128}$/.test(result?.previewId || '') || !Array.isArray(result.targets) || !result.targets.length || result.targets.length > 200) throw new Error('invalid preview response')
              previewId.value = result.previewId; targets.value = result.targets; visible.value = true; busy.value = false
              generation += 1; entry.value = makeEntry()
            } catch (error) { if (owns()) failure.value = ensureRequestFailure(error) }
            finally { if (owns()) busy.value = false }
          })()
          return false
        },
        checked(event) { if (owns() && !busy.value && previewId.value) confirmed.value = event?.target?.checked === true },
        async confirm() {
          if (!owns() || busy.value || !confirmed.value || !previewId.value) return
          const id = previewId.value, count = targets.value.length
          busy.value = true; failure.value = null
          try {
            const response = await confirmUserBanImport(id)
            if (!owns()) return
            if (response?.status !== 200 || response.data?.succeeded !== count) throw new Error('invalid confirmation response')
            reset(); notice.value = `已封禁 ${count} 个账号`
            generation += 1; entry.value = makeEntry(); emit('imported')
          } catch (error) {
            if (owns()) {
              failure.value = ensureRequestFailure(error); previewId.value = ''; confirmed.value = false; busy.value = false
              generation += 1; entry.value = makeEntry(); emit('imported')
            }
          } finally { if (owns()) busy.value = false }
        },
        cancel() {
          if (!owns() || busy.value) return
          const id = previewId.value
          reset()
          generation += 1; entry.value = makeEntry()
          if (id) void deleteUserBanImportPreview(id).catch(() => {})
        },
        template() {
          if (!owns() || busy.value) return
          const url = window.URL.createObjectURL(new Blob(['\uFEFFuser_id,reason,expires_at\r\n'], { type: 'text/csv;charset=utf-8' })), link = document.createElement('a')
          try { link.href = url; link.download = 'user-ban-import-template.csv'; document.body.appendChild(link); link.click() }
          finally { link.remove(); window.URL.revokeObjectURL(url) }
        },
      }
    }
    watch(() => currentUser.value, () => { generation += 1; reset(); entry.value = makeEntry() }, { immediate: true, flush: 'sync' })
    onBeforeUnmount(() => { alive = false; generation += 1 })
    return { canManage, visible, busy, confirmed, previewId, targets, notice, failure, entry, columns, formatTime, statusLabel }
  },
}
</script>
