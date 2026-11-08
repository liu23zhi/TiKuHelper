const {test,afterEach}=require('node:test')
const assert=require('node:assert/strict'),path=require('node:path')
const root=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
require(path.join(root,'tests/helpers/vue-dom.cjs'))
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,reactive}=require('vue'),Antd=require('ant-design-vue')
const {buildResponsePayload,captureTransportRequestError}=require(path.join(root,'src/utils/requestHelpers.mjs'))
const Alert=require(path.join(root,'src/components/RequestFailureAlert.vue'))
const ID='0123456789abcdef0123456789abcdef'
const failure=(status=503,id=ID)=>buildResponsePayload({status,data:{detail:'PRIVATE_DETAIL',requestId:'PRIVATE_BODY_ID'},headers:{'x-request-id':id}})
let wrapper,writes=[],clipboard=Object.getOwnPropertyDescriptor(navigator,'clipboard')
function clip(fn){Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:fn||((id)=>{writes.push(id);return Promise.resolve()})}})}
async function settle(){await nextTick();await flushPromises();await nextTick()}
function fixture(props={}){wrapper=mount(Alert,{props:{failure:failure(),message:'读取列表失败',retryable:true,contextKey:'A',...props},attrs:{class:'safe-extra'},attachTo:document.body,global:{plugins:[Antd]}});return wrapper}
const node=action=>wrapper.get('[data-action="'+action+'"]').element
afterEach(async()=>{wrapper?.unmount();wrapper=null;await settle();Antd.message.destroy();document.body.replaceChildren();writes=[];if(clipboard)Object.defineProperty(navigator,'clipboard',clipboard);else delete navigator.clipboard})
test('real alert shows only validated server ID and fixed copy; never auto retries, submits or dumps payload',async()=>{
 clip();fixture();await settle()
 assert.equal(wrapper.get('[data-request-diagnostic]').text(),ID)
 assert.doesNotMatch(wrapper.text(),/PRIVATE_|https:|Cookie|Authorization/)
 assert.equal(wrapper.emitted('retry'),undefined)
 assert.equal(wrapper.find('pre,textarea,form,[onclick]').exists(),false)
 assert.equal(node('retry-request').getAttribute('type'),'button')
 node('copy-request-diagnostic').click();await settle();assert.deepEqual(writes,[ID])
 assert.ok(wrapper.text().includes('诊断编号已复制'))
 node('retry-request').click();await settle()
 assert.equal(wrapper.emitted('retry').length,1)
})
test('Vue proxy is unwrapped for WeakMap metadata, without changing the enumerable payload',async()=>{
 const source=failure();clip();fixture({failure:reactive(source)});await settle()
 assert.equal(wrapper.get('[data-request-diagnostic]').text(),ID)
 assert.deepEqual(Object.keys(source).sort(),['data','message','status'])
})
test('missing or malformed transport header never produces a replacement ID/copy action',async()=>{
 for(const id of [undefined,'<svg/onload=PRIVATE_SCRIPT>',[ID],ID+'\n']){
  if(wrapper)wrapper.unmount()
  fixture({failure:id===undefined?buildResponsePayload({status:503,data:{},headers:{}}):failure(503,id)});await settle()
  assert.equal(wrapper.find('[data-request-diagnostic]').exists(),false)
  assert.equal(wrapper.find('[data-action="copy-request-diagnostic"]').exists(),false)
  assert.ok(wrapper.text().includes('不生成替代编号'))
 }
})
test('paid uncertainty, no-answer, balance and login denials never expose replay even if caller asks',async()=>{
 for(const f of [Object.assign(new Error('PRIVATE_URL'),{code:'UNKNOWN_OUTCOME'}),{status:401},{status:200,data:{error:{code:'NO_ANSWER'}}},{status:401,data:{code:'INSUFFICIENT_BALANCE'}}]){
  if(wrapper)wrapper.unmount()
  fixture({failure:f,retryable:true});await settle()
  assert.equal(wrapper.find('[data-action="retry-request"]').exists(),false)
  assert.doesNotMatch(wrapper.text(),/PRIVATE_URL/)
 }
})
test('same-tick parent lease retirement rejects old native copy/retry before child rerender',async()=>{
 let live=true;clip();fixture({isCurrent:()=>live});await settle()
 const oldCopy=node('copy-request-diagnostic'),oldRetry=node('retry-request')
 live=false;oldCopy.click();oldRetry.click();await settle()
 assert.deepEqual(writes,[]);assert.equal(wrapper.emitted('retry'),undefined)
})
test('context replacement and unmount retire captured native entry actions',async()=>{
 clip();fixture();await settle()
 const copy=node('copy-request-diagnostic'),retry=node('retry-request')
 await wrapper.setProps({contextKey:'B',failure:failure()});copy.click();retry.click();await settle()
 assert.deepEqual(writes,[]);assert.equal(wrapper.emitted('retry'),undefined)
 const nowCopy=node('copy-request-diagnostic'),nowRetry=node('retry-request')
 wrapper.unmount();nowCopy.click();nowRetry.click();await settle()
 assert.deepEqual(writes,[])
})
test('old clipboard resolution/rejection cannot paint or release the current copy state',async()=>{
 const pending=[];clip(id=>{writes.push(id);return new Promise((resolve,reject)=>pending.push({resolve,reject}))})
 fixture();await settle();node('copy-request-diagnostic').click();await settle()
 await wrapper.setProps({contextKey:'B',failure:failure()});node('copy-request-diagnostic').click();await settle()
 pending[0].resolve();await settle()
 assert.equal(node('copy-request-diagnostic').disabled,true)
 assert.equal(wrapper.text().includes('已复制'),false)
 pending[1].reject(new Error('PRIVATE_CLIPBOARD'));await settle()
 assert.equal(node('copy-request-diagnostic').disabled,false)
 assert.ok(wrapper.text().includes('手动选择'))
 assert.doesNotMatch(wrapper.text(),/PRIVATE_CLIPBOARD/)
})
test('busy and throwing lease hooks fail closed without dispatch or private error feedback',async()=>{
 clip();fixture({busy:true});await settle()
 node('copy-request-diagnostic').click();node('retry-request').click();await settle()
 assert.deepEqual(writes,[]);assert.equal(wrapper.emitted('retry'),undefined)
 await wrapper.setProps({busy:false,isCurrent:()=>{throw new Error('PRIVATE_LEASE')}})
 node('copy-request-diagnostic').click();node('retry-request').click();await settle()
 assert.deepEqual(writes,[]);assert.equal(wrapper.emitted('retry'),undefined);assert.doesNotMatch(wrapper.text(),/PRIVATE_LEASE/)
})
test('clipboard absence or exception is a safe manual-copy fallback, not a business failure',async()=>{
 delete navigator.clipboard;fixture();await settle()
 node('copy-request-diagnostic').click();await settle();assert.ok(wrapper.text().includes('手动选择'))
 assert.equal(wrapper.emitted('retry'),undefined)
 const e=Object.assign(new Error('PRIVATE_EXCEPTION'),{response:{status:403,headers:{'x-request-id':ID}}});captureTransportRequestError(e)
 await wrapper.setProps({failure:e});await settle()
 assert.equal(wrapper.get('[data-request-diagnostic]').text(),ID)
 assert.doesNotMatch(wrapper.text(),/PRIVATE_EXCEPTION/)
})
