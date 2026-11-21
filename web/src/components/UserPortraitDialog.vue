<template>
  <a-button type="link" :disabled="disabled" :onClick="bind(() => open = true)">画像</a-button>
  <a-modal v-if="open && canManage" :open="true" :title="`${user.nickname || user.username || '用户'}（#${user.id}）的画像`" :width="1100" :footer="null" :onCancel="bind(() => open = false)">
    <UserPortrait :user-id="Number(user.id)" embedded />
  </a-modal>
</template>
<script>
import { ref } from 'vue'
import UserPortrait from '@/pages/UserPortrait/index.vue'
import { useCurrentUserDisplay } from '@/utils/currentUser'
import { useAdminPanelLease } from '@/utils/useAdminPanelLease'
export default {
  components: { UserPortrait }, props: { user: { type: Object, required: true }, disabled: Boolean },
  setup(props) {
    const open = ref(false)
    const { currentUser } = useCurrentUserDisplay()
    const { canManage, bind } = useAdminPanelLease(currentUser, { resource: () => props.user.id, disabled: () => props.disabled, clear: () => { open.value = false } })
    return { open, canManage, bind }
  },
}
</script>
