<template>
  <section class="ledger-review" aria-label="有效金额核对">
    <h3>有效金额核对</h3>
    <p role="status" v-if="feedback">{{ feedback }}</p>
    <div v-if="review">
      <p>当前有效充值 <strong>¥ {{ review.totals.rechargeTotal }}</strong> · 历史累计到账本金 ¥ {{ review.totals.historicalRecharge }}</p>
      <p>当前有效消费 <strong>¥ {{ review.totals.consumptionTotal }}</strong> · 历史累计支出 ¥ {{ review.totals.historicalConsumption }}</p>
      <p>历史到账本金未扣退款；历史支出包含已记录的预扣和测试支出。当前有效消费只认确认的服务扣款，包含实际使用的赠金。</p>
      <p v-if="review.totals.confirmedUnallocatedRefund">账户已核销、未分摊到订单的本金：¥ {{ review.totals.confirmedUnallocatedRefund }}</p>
      <details v-if="review.totals.issues"><summary>历史待核：当前有效额不能作为完整结论</summary>
        <ul><li v-for="(n,k) in review.totals.issues" :key="k">{{ issueLabel(k) }}：{{ n }} 条</li></ul>
      </details>
      <details><summary>核对来源并修正有效额</summary>
        <p>修正仅影响有效累计、等级和等级设备上限，不动钱包和原始流水。一次仅调整一个来源；退款本金不能恢复。账务读写支持 0 至 10000000 元、最多六位小数，超界不重评。</p>
        <label>原始来源
          <select v-model="chosen" :disabled="busy" @change="resetDraft">
            <option value="">选择订单或扣款账单</option>
            <option v-for="s in review.sources" :key="keyOf(s)" :value="keyOf(s)">{{ s.kind === 'order' ? '充值订单' : '消费账单' }} #{{ s.id }} · {{ s.uncertain ? '金额待核、不可修正' : '有效 ¥ ' + s.effective }}</option>
          </select>
        </label>
        <button v-if="review.nextBill" type="button" :disabled="busy" @click="read(review.nextBill)">更早的账单</button>
        <template v-if="source">
          <p>原额 ¥ {{ source.gross }} <span v-if="source.debited != null">· 已实扣 ¥ {{ source.debited }}</span> <span v-if="source.kind === 'order'">· 可核实的已退本金 ¥ {{ source.refunded }}<span v-if="source.refundEstimateUpper"> · 另有未证实退款上界 ¥ {{ source.refundEstimateUpper }}（不是实退额）</span></span> <span v-if="!source.uncertain">· 当前来源贡献 ¥ {{ source.effective }}</span><span v-else>· 该来源贡献待核</span><span v-if="source.refundIds?.length"> · 退款单 #{{ source.refundIds.join('、#') }}</span></p>
          <p v-if="source.uncertain || review.totals.issues" role="status">账务存在待核事实，仅供核对，不能修正。</p>
          <template v-else><label>修正后{{ source.kind === 'order' ? '有效充值' : '有效消费' }}总额（元）<input v-model.trim="amount" inputmode="decimal" :disabled="busy"></label>
          <label>修正原因<input v-model.trim="reason" maxlength="255" :disabled="busy"></label>
          <button type="button" :disabled="busy || !reason || !amount" @click="save">单独保存本次账务修正</button></template>
        </template>
      </details>
      <details v-if="review.audit?.length"><summary>修正记录（{{ review.audit.length }}）</summary>
        <ul><li v-for="a in review.audit" :key="a.id">{{ a.createdAt }} · 操作人 #{{ a.operatorId }} · {{ a.sourceKind === 'order' ? '订单' : '账单' }} #{{ a.sourceId }} · ¥ {{ a.oldMicros / 1000000 }} → ¥ {{ a.newMicros / 1000000 }} · {{ a.reason }}</li></ul>
      </details>
    </div>
    <button type="button" :disabled="busy" @click="read(0)">{{ review ? '重新读取' : '读取当前有效金额与来源' }}</button>
  </section>
</template>
<script setup>
import {computed, onBeforeUnmount, ref} from 'vue'
import request from '@/utils/request'
const props=defineProps({userId:{type:Number,required:true}})
const review=ref(null),busy=ref(false),feedback=ref(''),chosen=ref(''),amount=ref(''),reason=ref('')
let alive=true
const keyOf=s=>s.kind+':'+s.id
const source=computed(()=>review.value?.sources?.find(s=>keyOf(s)===chosen.value))
const original=()=>source.value?.kind==='order'?review.value.totals.rechargeTotal:review.value.totals.consumptionTotal
const resetDraft=()=>{amount.value=source.value?String(original()):'';reason.value=''}
const labels={order_without_credit_proof:'订单缺少到账凭据',operator_refund_mark:'订单存在人工退款标记',settled_refund_without_consume_receipt:'已结算退款缺少本金核销流水',refund_without_exact_order_allocation:'退款无法精确分摊到订单',refund_without_credited_order:'退款找不到已到账订单',refund_principal_without_exact_receipt:'缺少可核实的实际本金，仅示上界',refund_wrong_owner_or_amount:'退款与本金核销归属不一致',refund_state_conflict:'退款主单与明细状态不一致',charge_without_exact_debit:'支出缺少与面额相符的实扣凭据',refund_exceeds_order_principal:'退款超出订单本金',unmatched_recharge_credit:'充值账单缺少匹配订单',billing_table_unavailable:'缺少账单表',order_table_unavailable:'缺少订单表',refund_table_unavailable:'缺少退款表'}
const issueLabel=k=>labels[k]||k.replace('unclassified_billing_type:','未识别的历史账单类型：')
const path=()=>'/api/admin/users/'+props.userId+'/effective-ledger'
async function requestData(options){
  const r=await request({url:path(),errorFeedback:'local',...options})
  if(r?.status!==200 || !r?.data?.data)throw new Error(r?.data?.message||r?.message||'账务请求失败')
  return r.data.data
}
async function read(cursor=0,success=''){
  busy.value=true
  try{const data=await requestData({method:'get',params:{beforeBill:cursor}});if(!alive)return
    review.value=data;chosen.value='';resetDraft();feedback.value=success
  }catch(e){if(alive){review.value=null;feedback.value=e?.response?.data?.message||e.message}}
  finally{if(alive)busy.value=false}
}
async function save(){
  if(busy.value||!source.value||source.value.uncertain||review.value?.totals?.issues)return
  busy.value=true
  try{
    await requestData({method:'post',data:{sourceKind:source.value.kind,sourceId:source.value.id,expected:original().toFixed(6),new:amount.value,reason:reason.value,revision:review.value.revision}})
    if(alive)await read(0,'已保存账务修正。')
  }catch(e){if(alive)feedback.value=e?.response?.data?.message||e.message}
  finally{if(alive)busy.value=false}
}
onBeforeUnmount(()=>{alive=false})
</script>
<style scoped>
.ledger-review{margin:12px 0 20px;padding:12px;border:1px solid #d5dbe3;border-radius:6px}.ledger-review label,.ledger-review details{display:block;margin:8px 0}.ledger-review input,.ledger-review select{display:block;width:100%;max-width:100%;padding:4px}.ledger-review summary,.ledger-review button{cursor:pointer}
</style>
