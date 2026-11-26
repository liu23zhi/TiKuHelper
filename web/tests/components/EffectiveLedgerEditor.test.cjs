const {test}=require('node:test')
const assert=require('node:assert/strict')
const path=require('node:path')
const Module=require('node:module')
require('../helpers/vue-dom.cjs')
const {mount,flushPromises}=require('@vue/test-utils')
const requestPath=path.resolve(__dirname,'../../src/utils/request.js')
const transport=new Module(requestPath)
const calls=[]
let corrected=false
transport.loaded=true
transport.exports=async req=>{
 calls.push(req)
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
