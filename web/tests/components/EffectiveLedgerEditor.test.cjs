const {test}=require('node:test')
const assert=require('node:assert/strict')
const path=require('node:path')
const Module=require('node:module')
require('../helpers/vue-dom.cjs')
const {mount,flushPromises}=require('@vue/test-utils')
const requestPath=path.resolve(__dirname,'../../src/utils/request.js')
const transport=new Module(requestPath)
const calls=[]
let corrected=false, wideCorrected=false
transport.loaded=true
transport.exports=async req=>{
 calls.push(req)
 if(req.url.includes("/users/45/")){
   const old='10009999998999.000000',fresh='10009999998998.999999'
   if(req.method==='post'){wideCorrected=true;return {status:200,data:{data:{id:201}}}}
   return {status:200,data:{data:{revision:wideCorrected?92:91,totals:{exact:{recharge:'0.000000',consumption:wideCorrected?fresh:old,historicalRecharge:'0.000000',historicalConsumption:old}},sources:[{kind:'bill',id:99,exact:{gross:'9999999999.00000000',effective:'9999999999.000000'}}],audit:wideCorrected?[{id:201,sourceKind:'bill',sourceId:99,oldExact:old,newExact:fresh,reason:'单笔纠错'}]:[]}}}
 }
 if(req.url.includes("/users/44/")){
   if(req.method==='post')return {status:200,data:{data:{id:200}}}
   return {status:200,data:{data:{revision:90,totals:{rechargeTotal:11000000000.000002,historicalRecharge:12000000000,consumptionTotal:0,historicalConsumption:0,exact:{recharge:'11000000000.000001',historicalRecharge:'12000000000.000000',consumption:'0.000000',historicalConsumption:'0.000000'}},sources:[{kind:'order',id:88,gross:6000000000,effective:5000000000,refunded:0}],audit:[]}}}
 }
 if(req.url.includes("/users/42/"))return {status:200,data:{data:{totals:{rechargeTotal:5.85,historicalRecharge:10,consumptionTotal:0,historicalConsumption:0,issues:{refund_principal_without_exact_receipt:1}},sources:[{kind:"order",id:5,gross:10,refunded:0,refundEstimateUpper:4.15,uncertain:true,effective:5.85,refundIds:[71]}],audit:[]}}}
 if(req.method==='post'){corrected=true;return {status:200,data:{data:{id:91}}}}
 return {status:200,data:{data:{revision:corrected?91:0,totals:{rechargeTotal:6,historicalRecharge:10,consumptionTotal:corrected?1:2,historicalConsumption:2},sources:[{kind:'bill',id:11,gross:2,refunded:0,effective:corrected?1:2}],audit:[]}}}
}
require.cache[requestPath]=transport
const Editor=require('../../src/components/EffectiveLedgerEditor.vue')
test('shows original facts, sends a reasoned correction tied to the chosen bill and reloads its current total',async()=>{
 const w=mount(Editor,{props:{userId:9}})
 try{
  assert.equal(calls.length,0);await w.get('button').trigger('click');await flushPromises()
  assert.match(w.text(),/历史累计到账本金 ¥ 10/)
  await w.get('select').setValue('bill:11')
  assert.match(w.text(),/当前来源贡献 ¥ 2/)
  await w.findAll('input')[0].setValue('1.000000')
  const save=w.findAll('button').find(x=>x.text().includes('保存本次'))
  assert.equal(save.element.disabled,true)
  await w.findAll('input')[1].setValue('核实错误扣费')
  await save.trigger('click');await flushPromises()
  assert.deepEqual(calls.map(x=>x.method),['get','post','get'])
  assert.equal(calls[1].url,'/api/admin/users/9/effective-ledger')
  assert.deepEqual(calls[1].data,{sourceKind:'bill',sourceId:11,expected:'2.000000',new:'1.000000',reason:'核实错误扣费',revision:0})
  assert.match(w.text(),/当前有效消费 ¥ 1/)
 }finally{w.unmount()}
})


test('an estimate can be inspected but never labeled a confirmed refund or saved',async()=>{
 const w=mount(Editor,{props:{userId:42}})
 try{
  await w.get('button').trigger('click');await flushPromises()
  await w.get('select').setValue('order:5')
  assert.match(w.text(),/可核实的已退本金 ¥ 0/)
  assert.match(w.text(),/未证实退款上界 ¥ 4.15（不是实退额）/)
  assert.match(w.text(),/该来源贡献待核/)
  assert.doesNotMatch(w.text(),/已退本金 ¥ 4.15|单独保存本次账务修正/)
 }finally{w.unmount()}
})


test('sends the server decimal expected value instead of reconstructing it from Number',async()=>{
 const w=mount(Editor,{props:{userId:44}})
 try{
  await w.get('button').trigger('click');await flushPromises();await w.get('select').setValue('order:88')
  assert.equal(w.findAll('input')[0].element.value,'11000000000.000001')
  await w.findAll('input')[0].setValue('11000000000.000000');await w.findAll('input')[1].setValue('合成一微元')
  await w.findAll('button').find(x=>x.text().includes('保存本次')).trigger('click');await flushPromises()
  const post=calls.find(x=>x.method==='post'&&x.url.includes('/44/'));assert.equal(post.data.expected,'11000000000.000001');assert.equal(post.data.new,'11000000000.000000')
 }finally{w.unmount()}
})


test('a legitimate source can correct an above-int64-micros account total and show its exact reloaded audit',async()=>{
 const w=mount(Editor,{props:{userId:45}})
 try{
  await w.get('button').trigger('click');await flushPromises();await w.get('select').setValue('bill:99')
  assert.equal(w.findAll('input')[0].element.value,'10009999998999.000000')
  await w.findAll('input')[0].setValue('10009999998998.999999');await w.findAll('input')[1].setValue('单笔纠错')
  await w.findAll('button').find(b=>b.text().includes('保存本次')).trigger('click');await flushPromises()
  const post=calls.find(x=>x.method==='post'&&x.url.includes('/45/'));assert.equal(post.data.expected,'10009999998999.000000');assert.equal(post.data.new,'10009999998998.999999')
  assert.match(w.text(),/10009999998999.000000 → ¥ 10009999998998.999999/)
 }finally{w.unmount()}
})
