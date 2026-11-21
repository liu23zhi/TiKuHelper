<template>
 <section v-if="eligible" class="ticket-account-actions">
  <h3>账号处理</h3>
  <p>处理账号 {{ accountLabel }}（ID：{{ targetId }}）</p>
  <a-alert v-if="error" type="error" :message="error" show-icon />
  <a-form :model="form" layout="vertical" :disabled="busy" @finish="submit">
   <a-form-item label="处理动作"><a-select v-model:value="form.action" :options="options" /></a-form-item>
   <a-form-item v-if="form.action==='phone'||form.action==='wechat'" :label="form.action==='phone'?'新的手机号（留空解绑）':'新的微信 ID（留空解绑）'"><a-input v-model:value="form.value" autocomplete="off" /><small>已被其他账号占用的绑定不会被强制抢占。</small></a-form-item>
   <a-form-item v-if="form.action==='ban'" label="封禁时长（小时，0 为永久）"><a-input-number v-model:value="form.durationAmount" :min="0" :precision="0" /></a-form-item>
   <a-form-item label="处理原因"><a-textarea v-model:value="form.reason" :rows="2" /></a-form-item>
   <a-button type="primary" html-type="submit" :loading="busy" :disabled="!form.reason.trim()">确认处理</a-button>
  </a-form>
 </section>
</template>
<script setup>
import {computed,reactive,ref,watch,onBeforeUnmount} from 'vue'
import {Modal,message} from 'ant-design-vue'
import {submitTicketAccountAction} from '@/api/TicketApi'
import {useCurrentUserDisplay,isAdminUser,getCurrentUserLogoutEpoch} from '@/utils/currentUser'
import {currentUserIdentity} from '@/utils/currentUserScope'
const props=defineProps({ticket:{type:Object,default:null},appealTarget:{type:Object,default:null},active:{type:Boolean,default:true}}),emit=defineEmits(['updated'])
const {currentUser}=useCurrentUserDisplay(),busy=ref(false),error=ref(''),form=reactive({action:'unban',value:'',reason:'',durationAmount:24})
const options=[{value:'unban',label:'解除账号封禁'},{value:'ban',label:'封禁账号'},{value:'phone',label:'绑定 / 解绑手机号'},{value:'wechat',label:'绑定 / 解绑微信'}]
let alive=true,version=0,confirmation=null
const targetId = computed(() => props.appealTarget?.id ?? props.ticket?.userId)
const accountLabel = computed(() => props.appealTarget?.username || props.ticket?.userName)
const eligible = computed(() => alive && props.active && isAdminUser(currentUser.value) &&
  !['banned', 'disabled'].includes(String(currentUser.value?.status || '')) &&
  !['archived', 'missing'].includes(props.appealTarget?.status) && targetId.value &&
  ['account', 'account_rebind', 'blacklist_appeal'].includes(props.ticket?.category))

async function submit() {
  if (!eligible.value || busy.value || confirmation || !form.reason.trim()) return
  const key = version
  const ticketId = props.ticket.id
  const userId = targetId.value
  const logout = getCurrentUserLogoutEpoch()
  const payload = { ...form, reason: form.reason.trim(), durationUnit: 'hour' }
  const owns = () => eligible.value && key === version && ticketId === props.ticket?.id &&
    userId === targetId.value && logout === getCurrentUserLogoutEpoch()
  confirmation = Modal.confirm({
    title: `确认处理账号 ${accountLabel.value || userId}？`,
    content: `账号 ID ${userId}`,
    onCancel: () => { confirmation = null },
    onOk: async () => {
      confirmation = null
      if (!owns()) return
      busy.value = true
      error.value = ''
      try {
        await submitTicketAccountAction(ticketId, payload)
        if (!owns()) return
        message.success('账号处理完成')
        emit('updated')
      } catch (failure) {
        if (owns()) error.value = failure?.data?.message || failure?.message || '账号处理失败，请核对后重试'
      } finally {
        if (owns()) busy.value = false
      }
    },
  })
}

watch(() => [props.active, props.ticket?.id, targetId.value, props.appealTarget?.status,
  currentUserIdentity(currentUser.value), currentUser.value?.role, currentUser.value?.status, getCurrentUserLogoutEpoch()], () => {
  version++
  confirmation?.destroy()
  confirmation = null
  busy.value = false
  error.value = ''
  Object.assign(form, { action: 'unban', value: '', reason: '', durationAmount: 24 })
}, { flush: 'sync' })
onBeforeUnmount(()=>{alive=false;version++;confirmation?.destroy()})
</script>
<style scoped>.ticket-account-actions{margin:20px 0;padding:20px;border:1px solid var(--console-border);border-radius:12px}.ticket-account-actions h3{margin-top:0}.ticket-account-actions .ant-select{width:100%}</style>
