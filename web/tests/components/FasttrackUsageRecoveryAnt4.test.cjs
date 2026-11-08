const { test } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const current = require(path.join(web, 'src/utils/currentUser.js'))
const page = process.env.USAGE_PAGE_FILE || path.join(web, 'src/pages/UserUsage/index.vue')
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const user = { id: 708, username: 'synthetic-usage-recovery', role: 'user', balance: 1.000062 }
const method = { id: 17, enabled: true, name: '测试方式', epayType: 'fixture', iconSvg: '' }
const bill = id => ({ id, type: 'normal_search', direction: 'expense', amount: 0.000123, subject: '测试账单' + id, questionText: '测试账单' + id, provider: 'local', requestId: 'fixture-' + id, createdAt: '2026-10-28T00:00:00+08:00' })
const response = rows => ({ status: 200, data: { data: rows, total: rows.length, page: 1, pageSize: 10 } })
let active
function deferred() { let resolve, reject; const promise = new Promise((a,b) => {resolve=a;reject=b}); return {promise,resolve,reject} }
function take(key, fallback) {
  active.calls[key]++
  const item = active[key].length ? active[key].shift() : fallback()
  return typeof item === 'function' ? item() : Promise.resolve(item)
}
const api = new Module(apiPath)
api.loaded = true
api.exports = {
 getCurrentUser: () => take('users', () => ({status:200,data:user})),
 getBillingRecords: params => { active.billParams.push(params); return take('bills', () => response([])) },
 getPaymentMethods: () => take('payments', () => ({status:200,data:[method]})),
 createRechargeOrder: () => { active.posts++; return Promise.resolve({status:500,message:'SYNTHETIC_NO_CHECKOUT'}) },
}
require.cache[apiPath] = api
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function run(options, body) {
 active = {users:[],bills:[],payments:[],billParams:[],calls:{users:0,bills:0,payments:0},posts:0,...options}
 current.setStoredCurrentUser(user)
 delete require.cache[page]
 const Page = require(page)
 const wrappers = []
 const previousFetch = globalThis.fetch
 globalThis.fetch = () => { throw new Error('NO_LIVE_NETWORK_ALLOWED') }
 const add = async () => { const w=mount(Page,{attachTo:document.body,global:{plugins:[Antd]}});wrappers.push(w);await settle();return w }
 try { await body(await add(),active,add) }
 finally {
  wrappers.reverse().forEach(w => w.unmount())
  await settle(); Antd.message.destroy(); Antd.Modal.destroyAll()
  current.setStoredCurrentUser(null);document.body.replaceChildren();globalThis.fetch=previousFetch
  delete require.cache[page];active=null
 }
}
function button(w, label) { const b=w.findAll('button').find(b=>b.text().replace(/\s/g,'')===label);assert.ok(b,'native button '+label);return b }
function orderButton(w) { return button(w,'创建充值订单') }
test('U06C failed balance read stays visible after toast removal and retries only its read with six decimals', async () => {
 await run({users:[()=>Promise.reject(new Error('SYNTHETIC_BALANCE_FAILURE'))]},async(w,s)=>{
  Antd.message.destroy();await settle()
  assert.ok(w.find('.balance-refresh-error').exists(),'persistent balance failure must not be swallowed')
  assert.equal(w.vm.currentUserBalance,1.000062)
  s.users.push({status:200,data:{...user,balance:0.000123}})
  await button(w,'重试余额刷新').trigger('click');await settle()
  assert.equal(w.find('.balance-refresh-error').exists(),false)
  assert.match(w.find('.balance-value').text(),/0\.000123/)
  assert.deepEqual(s.calls,{users:2,bills:1,payments:1});assert.equal(s.posts,0)
 })
})
test('U06C failed billing is not a successful empty statement and targeted retry recovers records', async () => {
 await run({bills:[()=>Promise.reject(new Error('SYNTHETIC_BILL_FAILURE'))]},async(w,s)=>{
  Antd.message.destroy();await settle()
  assert.ok(w.find('.billing-load-error').exists(),'failed billing must have persistent recovery')
  assert.equal(w.text().includes('当前还没有消费记录。'),false,'a failed read is not empty success')
  s.bills.push(response([bill(81)]))
  await button(w,'重试账单加载').trigger('click');await settle()
  assert.equal(w.find('.billing-load-error').exists(),false);assert.equal(w.vm.dataSource[0].id,81)
  assert.deepEqual(s.calls,{users:1,bills:2,payments:1});assert.equal(s.posts,0)
 })
})
test('U06C failed payment-method refresh disables old choices and recovery performs no order', async () => {
 await run({},async(w,s)=>{
  assert.equal(orderButton(w).element.disabled,false)
  s.payments.push(()=>Promise.reject(new Error('SYNTHETIC_METHOD_FAILURE')))
  await w.vm.handleLoadPaymentMethods();await settle();Antd.message.destroy();await settle()
  assert.ok(w.find('.payment-method-load-error').exists())
  assert.equal(orderButton(w).element.disabled,true,'stale choices cannot authorize a new order')
  await w.vm.handleCreateRechargeOrder();await settle();assert.equal(s.posts,0)
  await button(w,'重试支付方式').trigger('click');await settle()
  assert.equal(w.find('.payment-method-load-error').exists(),false);assert.equal(orderButton(w).element.disabled,false)
  assert.deepEqual(s.calls,{users:1,bills:1,payments:3});assert.equal(s.posts,0)
 })
})
test('U06C stale billing error and finally cannot clear a newer request busy state',async()=>{
 await run({},async(w,s)=>{
  const old=deferred(),fresh=deferred();s.bills.push(()=>old.promise,()=>fresh.promise)
  const a=w.vm.handleLoadBillingRecords(),b=w.vm.handleLoadBillingRecords()
  old.reject(new Error('STALE_FAILURE'));await a;await settle()
  assert.equal(w.vm.tableLoading,true,'only latest same-account read releases loading')
  assert.equal(w.find('.billing-load-error').exists(),false)
  fresh.resolve(response([bill(82)]));await b;await settle()
  assert.equal(w.vm.tableLoading,false);assert.equal(w.vm.dataSource[0].id,82)
 })
})
test('U06C late billing success cannot overwrite the most recent same-account records',async()=>{
 await run({},async(w,s)=>{
  const old=deferred(),fresh=deferred();s.bills.push(()=>old.promise,()=>fresh.promise)
  const a=w.vm.handleLoadBillingRecords(),b=w.vm.handleLoadBillingRecords()
  fresh.resolve(response([bill(83)]));await b;old.resolve(response([bill(84)]));await a;await settle()
  assert.equal(w.vm.dataSource[0].id,83)
 })
})
test('U06C late payment-method response cannot re-enable a newer empty configuration',async()=>{
 await run({},async(w,s)=>{
  const old=deferred(),fresh=deferred();s.payments.push(()=>old.promise,()=>fresh.promise)
  const a=w.vm.handleLoadPaymentMethods(),b=w.vm.handleLoadPaymentMethods()
  fresh.resolve({status:200,data:[]});await b;old.resolve({status:200,data:[method]});await a;await settle()
  assert.equal(w.vm.paymentMethods.length,0);assert.equal(orderButton(w).element.disabled,true);assert.equal(s.posts,0)
 })
})
test('U06C independent page instances do not share billing or selected payment state',async()=>{
 await run({bills:[response([bill(85)]),response([bill(86)])]},async(first,s,add)=>{
  const second=await add()
  assert.equal(first.vm.dataSource[0].id,85,'second instance cannot publish into first')
  assert.equal(second.vm.dataSource[0].id,86)
  first.vm.selectedPaymentMethodId=null;await settle()
  assert.equal(second.vm.selectedPaymentMethodId,17)
 })
})
test('U06C nonpositive six-decimal balance explains paid-call admission without disabling recharge',async()=>{
 await run({users:[{status:200,data:{...user,balance:-0.000001}}]},async(w,s)=>{
  assert.ok(w.find('.balance-warning').exists(),'low balance needs explicit admission feedback')
  assert.match(w.find('.balance-value').text(),/-0\.000001/)
  assert.equal(orderButton(w).element.disabled,false,'nonpositive wallet must still allow recharge')
  assert.equal(s.posts,0)
 })
})

test('U06C principal switch clears prior failure and resets only that principal query to page one', async () => {
 await run({}, async(w,s) => {
  w.vm.pagination.current=4
  s.bills.push(()=>Promise.reject(new Error('SYNTHETIC_OLD_PAGE_FAILURE')))
  await w.vm.handleLoadBillingRecords();await settle()
  assert.equal(w.vm.pagination.current,4)
  current.setStoredCurrentUser({...user,id:709,username:'synthetic-new-principal',balance:5.000062})
  await settle()
  assert.equal(s.billParams.at(-1).page,1,'new principal must not inherit old page4')
  assert.equal(w.find('.billing-load-error').exists(),false)
  assert.equal(s.posts,0)
 })
})
