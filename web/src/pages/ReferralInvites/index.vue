<template>
 <div class="console-page">
  <ConsolePageHeading title="邀请码管理" description="查看全站账户的邀请码与邀请人数。邀请码公开可分享，不能作为登录或接口凭据。" />
  <ConsolePanel title="邀请码列表">
   <a-input-search v-model:value="keyword" placeholder="邀请码 / 用户名 / 昵称" @search="search" style="max-width:400px;margin-bottom:16px" />
   <a-alert v-if="error" :message="error" type="error" show-icon />
   <a-table :columns="columns" :data-source="rows" :loading="loading" row-key="userId" :pagination="pagination" @change="changePage" :scroll="{x:800}">
    <template #bodyCell="{column,record}"><span v-if="column.key==='createdAt'">{{ record.createdAt ? new Date(record.createdAt).toLocaleString() : '—' }}</span></template>
   </a-table>
  </ConsolePanel>
 </div>
</template>
<script setup>
defineOptions({ name: 'ReferralInvitesPage' })
import {computed,onMounted,onBeforeUnmount,ref,watch} from 'vue'
import ConsolePageHeading from '@/components/ConsolePageHeading.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import request from '@/utils/request'
import {useCurrentUserDisplay,isAdminUser,getCurrentUserLogoutEpoch} from '@/utils/currentUser'
import {currentUserIdentity} from '@/utils/currentUserScope'
const {currentUser}=useCurrentUserDisplay(),keyword=ref(''),applied=ref(''),rows=ref([]),page=ref(1),total=ref(0),loading=ref(false),error=ref('')
let alive=true,revision=0
const columns=[{title:'用户 ID',dataIndex:'userId'},{title:'用户名',dataIndex:'username'},{title:'昵称',dataIndex:'nickname'},{title:'邀请码',dataIndex:'code'},{title:'邀请人数',dataIndex:'invitedCount'},{title:'生成时间',key:'createdAt'}]
const pagination=computed(()=>({current:page.value,pageSize:20,total:total.value}))
async function load(){if(!alive||!isAdminUser(currentUser.value))return;const key=++revision,identity=currentUserIdentity(currentUser.value),logout=getCurrentUserLogoutEpoch();const owns=()=>alive&&key===revision&&isAdminUser(currentUser.value)&&identity===currentUserIdentity(currentUser.value)&&logout===getCurrentUserLogoutEpoch();loading.value=true;error.value='';try{const res=await request({url:'/api/admin/referral-invites',method:'get',params:{keyword:applied.value,page:page.value,pageSize:20},errorFeedback:'local'});if(!owns())return;if(res.status!==200)throw res;rows.value=res.data.data||[];total.value=res.data.total||0}catch{if(owns())error.value='邀请码读取失败，请重新查询'}finally{if(owns())loading.value=false}}
function search(){applied.value=keyword.value.trim();page.value=1;void load()}
function changePage(value){page.value=value.current;void load()}
watch(()=>[currentUserIdentity(currentUser.value),currentUser.value?.role,getCurrentUserLogoutEpoch()],()=>{revision++;rows.value=[];total.value=0;loading.value=false;error.value='';if(isAdminUser(currentUser.value))void load()},{flush:'sync'})
onMounted(load);onBeforeUnmount(()=>{alive=false;revision++;rows.value=[]})
</script>
