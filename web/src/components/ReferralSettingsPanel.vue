<template>
  <ConsolePanel title="邀请码与邀请返佣" description="每个注册账号自动获得邀请码。返佣按受邀用户实际充值本金计算，结算进入不可退款的赠送余额。">
    <a-alert v-if="error" type="error" show-icon :message="error" />
    <a-form :model="form" layout="vertical" :disabled="disabled || loading || saving" @finish="save">
      <a-form-item label="启用邀请返佣"><a-switch v-model:checked="form.enabled" /></a-form-item>
      <a-form-item label="返佣比例（%）"><a-input-number v-model:value="form.percent" :min="0" :max="99.9999" :precision="4" /><p>关闭返佣仍可使用邀请码记录邀请关系；未配置比例时不产生返佣。</p></a-form-item>
      <a-space><a-button type="primary" html-type="submit" :disabled="!loaded || disabled" :loading="saving">保存邀请配置</a-button><a-button :loading="loading" @click="load">重新读取</a-button></a-space>
    </a-form>
  </ConsolePanel>
</template>
<script setup>
import { onMounted, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import request from '@/utils/request'
import { currentUserIdentity } from '@/utils/currentUserScope'
import { useCurrentUserDisplay, getCurrentUserLogoutEpoch, isAdminUser } from '@/utils/currentUser'
const props=defineProps({disabled:Boolean})
const {currentUser}=useCurrentUserDisplay(), form=reactive({enabled:false,percent:0}),loading=ref(false),saving=ref(false),loaded=ref(false),error=ref('')
let alive=true,version=0
const allowed=()=>alive&&!props.disabled&&isAdminUser(currentUser.value)&&!['banned','disabled'].includes(String(currentUser.value?.status||''))
const owner=()=>{const key=version,identity=currentUserIdentity(currentUser.value),logout=getCurrentUserLogoutEpoch();return()=>allowed()&&key===version&&identity===currentUserIdentity(currentUser.value)&&logout===getCurrentUserLogoutEpoch()}
async function load(){if(!allowed()||loading.value||saving.value)return;const owns=owner();loading.value=true;error.value='';try{const res=await request({url:'/api/admin/referral-settings',method:'get',errorFeedback:'local'});if(!owns())return;if(res.status!==200)throw res;Object.assign(form,{enabled:res.data.enabled===true,percent:Number(res.data.rate)*100});loaded.value=true}catch(e){if(owns())error.value=e?.data?.message||'邀请配置读取失败'}finally{if(owns())loading.value=false}}
async function save(){if(!allowed()||!loaded.value||saving.value)return;const owns=owner();saving.value=true;error.value='';try{const res=await request({url:'/api/admin/referral-settings',method:'put',errorFeedback:'local',data:{enabled:form.enabled,rate:Number(form.percent)/100}});if(!owns())return;if(res.status!==200)throw res;message.success('邀请配置已保存')}catch(e){if(owns())error.value=e?.data?.message||'邀请配置保存失败'}finally{if(owns())saving.value=false}}
watch(()=>[props.disabled,currentUserIdentity(currentUser.value),currentUser.value?.role,currentUser.value?.status,getCurrentUserLogoutEpoch()],()=>{version++;loaded.value=loading.value=saving.value=false;error.value='';Object.assign(form,{enabled:false,percent:0});if(allowed())void load()},{flush:'sync'})
onMounted(load);onBeforeUnmount(()=>{alive=false;version++})
</script>
