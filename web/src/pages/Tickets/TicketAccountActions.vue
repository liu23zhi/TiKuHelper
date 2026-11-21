<template>
 <section v-if="eligible" class="ticket-account-actions">
  <h3>账号处理</h3>
  <p>操作仅作用于本工单所属用户（ID：{{ ticket.userId }}），并记录管理员处理原因。</p>
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
import request from '@/utils/request'
import {useCurrentUserDisplay,isAdminUser,getCurrentUserLogoutEpoch} from '@/utils/currentUser'
import {currentUserIdentity} from '@/utils/currentUserScope'
const props=defineProps({ticket:{type:Object,default:null},active:{type:Boolean,default:true}}),emit=defineEmits(['updated'])
const {currentUser}=useCurrentUserDisplay(),busy=ref(false),error=ref(''),form=reactive({action:'unban',value:'',reason:'',durationAmount:24})
const options=[{value:'unban',label:'解除账号封禁'},{value:'ban',label:'封禁账号'},{value:'phone',label:'绑定 / 解绑手机号'},{value:'wechat',label:'绑定 / 解绑微信'}]
let alive=true,version=0,confirmation=null
const eligible=computed(()=>alive&&props.active&&isAdminUser(currentUser.value)&&!['banned','disabled'].includes(String(currentUser.value?.status||''))&&['account','account_rebind','blacklist_appeal'].includes(props.ticket?.category))
async function submit(){if(!eligible.value||busy.value||!form.reason.trim())return;const key=version,id=props.ticket.id,identity=currentUserIdentity(currentUser.value),logout=getCurrentUserLogoutEpoch(),payload={...form,reason:form.reason.trim(),durationUnit:'hour'};const owns=()=>eligible.value&&key===version&&id===props.ticket?.id&&identity===currentUserIdentity(currentUser.value)&&logout===getCurrentUserLogoutEpoch();confirmation=Modal.confirm({title:'确认处理本工单所属账号？',content:'请核对工单与用户身份；绑定操作不改变账号资料、余额或权限。',onOk:async()=>{if(!owns())return;busy.value=true;error.value='';try{const res=await request({url:`/api/tickets/${id}/account-action`,method:'post',data:payload,errorFeedback:'local'});if(!owns())return;if(res.status!==200)throw res;message.success('账号处理完成');emit('updated')}catch(e){if(owns())error.value=e?.data?.message||'账号处理失败，请核对后重试'}finally{if(owns())busy.value=false}}})}
watch(()=>[props.active,props.ticket?.id,currentUserIdentity(currentUser.value),currentUser.value?.role,currentUser.value?.status,getCurrentUserLogoutEpoch()],()=>{version++;confirmation?.destroy();confirmation=null;busy.value=false;error.value='';Object.assign(form,{action:'unban',value:'',reason:'',durationAmount:24})},{flush:'sync'})
onBeforeUnmount(()=>{alive=false;version++;confirmation?.destroy()})
</script>
<style scoped>.ticket-account-actions{margin:20px 0;padding:20px;border:1px solid var(--console-border);border-radius:12px}.ticket-account-actions h3{margin-top:0}.ticket-account-actions .ant-select{width:100%}</style>
