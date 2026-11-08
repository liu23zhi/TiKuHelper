/* Real UserList/Antd/API/request with inert allowlisted Axios transport.
 * Synthetic client feedback contract, not backend authorization or real deletion proof. */
const {test,afterEach,after}=require('node:test')
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
assert.ok(path.isAbsolute(web),'absolute source web required')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
const helper=process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs')
require(helper);require.extensions['.css']=m=>{m.exports={}};globalThis.getComputedStyle=window.getComputedStyle
for(const key of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','CustomEvent'])globalThis[key]=window[key]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,h,toRaw}=require('vue'),{createRouter,createMemoryHistory,RouterView}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const zhCN=require('ant-design-vue/lib/locale/zh_CN').default,user=require(path.join(web,'src/utils/currentUser.js'))
const A={id:65101,username:'SYNTHETIC_DELETE_ADMIN_A',role:'admin',status:'enabled'},B={id:65102,username:'SYNTHETIC_DELETE_ADMIN_B',role:'admin',status:'enabled'}
const UNSAFE='SYNTHETIC_UNSAFE_DELETE_DETAIL_PRIVATE',ROWS=[{id:71,username:'SYNTHETIC_DELETE_TARGET_71',nickname:'合成用户一',role:'user',status:'enabled',balance:1.234567,phone:'',wechatId:'',perms:'search',registerSource:'admin',lastLoginIp:'',registerIp:''},{id:72,username:'SYNTHETIC_DELETE_TARGET_72',nickname:'合成用户二',role:'user',status:'enabled',balance:2,phone:'',wechatId:'',perms:'search',registerSource:'admin',lastLoginIp:'',registerIp:''}]
const cases=[],roots=[],calls=[],intents=[],attempts=[],replies=[],plans=[],pending=[],copies=[],pendingCopies=[],IDs=new Set(),originalErrors=new Map(),wireResponses=new Map(),realIO=[],runtime=[],consoleErrors=[],consoleWarnings=[]
let cur=null,serial=0,intentSerial=0,querySerial=0,copyMode='resolve',rows=ROWS.map(r=>({...r})),restoreRemove=null
const compact=x=>String(x??'').replace(/\s+/g,' ').trim(),hash=x=>crypto.createHash('sha256').update(x).digest('hex'),H=p=>hash(fs.readFileSync(p))
const inputs=['src/pages/UserList/index.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/requestHelpers.mjs','src/utils/requestRetry.mjs','src/utils/requestFailureView.mjs','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/userPortalHelpers.mjs','src/utils/listSearchHelpers.mjs','src/components/RequestFailureAlert.vue','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputs.map(p=>[p,H(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=H(__filename)
function check(value,label,category='independent',qualified=true){cur.checks.push({label,category,qualified,passed:!!value})}
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_DELETE_DIAGNOSTIC_IO')}
const originalAdapter=axios.defaults.adapter
assert.equal(!!require.cache[path.join(web,'src/utils/request.js')],false,'real request must load after inert adapter')
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url),actor=user.getStoredCurrentUser(),entry={method,url,actor:actor?.id||null,page:config.params?.page||null,pageSize:config.params?.pageSize||null,hasKeyword:!!config.params?.keyword};calls.push(entry)
 if(method==='get'&&url==='/adapter-service/user')return Promise.resolve({status:200,data:{data:rows.map(r=>({...r})),total:21,page:Number(config.params?.page)||1,pageSize:Number(config.params?.pageSize)||10},headers:{},config,request:{memoryOnly:true}})
 if(method==='get'&&url==='/adapter-service/system-settings')return Promise.resolve({status:200,data:{newUserBonusAmount:0},headers:{},config,request:{memoryOnly:true}})
 if(method!=='delete'||!/^\/adapter-service\/user\/(71|72)$/.test(url))return Promise.reject(deny('unexpected-api-operation'))
 const target=Number(url.split('/').pop()),plan=plans.shift(),intent=intents.find(i=>i.target===target&&!i.wire)
 assert.ok(plan&&intent,'every destructive native dispatch has one explicit synthetic plan and API intent')
 assert.equal(plan.target,target,'planned native target is exact');intent.wire=true;plan.intent=intent.sequence
 const id=plan.noID?'':(++serial).toString(16).padStart(32,'0'),status=plan.status||500
 const attempt={intent:intent.sequence,target,id,status,mode:plan.mode||'response',hasBody:config.data!==undefined&&config.data!==null&&config.data!=='',terminal:false};attempts.push(attempt)
 const response={status,data:{message:status===200?'删除用户成功':UNSAFE,detail:status===200?'':UNSAFE},headers:id?{'x-request-id':id}:{},config,request:{memoryOnly:true}}
 wireResponses.set(intent.sequence,response)
 const error=new Error(UNSAFE)
 if(plan.mode==='network'){error.code='ERR_NETWORK'}else if(plan.mode==='reject'){error.response=response;error.config=config;error.code='ERR_BAD_RESPONSE'}
 originalErrors.set(intent.sequence,error)
 const finish=(resolve,reject)=>{attempt.terminal=true;if(plan.mode==='reject'||plan.mode==='network')reject(error);else{if(status===200)rows=rows.filter(r=>r.id!==target);resolve(response)}}
 if(plan.hold){let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});const holder={intent:intent.sequence,done:false,finish:()=>{if(!holder.done){holder.done=true;finish(resolve,reject)}}};pending.push(holder);return promise}
 return new Promise(finish)
}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:value=>{const valid=typeof value==='string'&&/^[0-9a-f]{32}$/.test(value)&&IDs.has(value);copies.push({id:valid?value:'[INVALID_DIAGNOSTIC]',validReturnedIDOnly:valid});if(!valid)return Promise.reject(new Error('NON_DIAGNOSTIC_CLIPBOARD_REFUSED'));if(copyMode==='defer')return new Promise((resolve,reject)=>pendingCopies.push({id:value,resolve,reject,done:false}));return Promise.resolve()}}})
document.execCommand=()=>deny('clipboard-fallback')
window.addEventListener('error',e=>runtime.push(e.error?.name||'window-error'));window.addEventListener('unhandledrejection',e=>runtime.push(e.reason?.name||'unhandled-rejection'))
const oldConsoleError=console.error,oldConsoleWarn=console.warn
console.error=(...args)=>{consoleErrors.push({argumentCount:args.length});return Reflect.apply(oldConsoleError,console,args)}
console.warn=(...args)=>{consoleWarnings.push({argumentCount:args.length});return Reflect.apply(oldConsoleWarn,console,args)}
// API and its Axios instance are created only after inert adapter and real-IO denies.
const api=require(path.join(web,'src/api/TikuComponentApi.js')),originalDelete=api.delUser,transport=require(path.join(web,'src/utils/requestHelpers.mjs')),failure=require(path.join(web,'src/utils/requestFailureView.mjs'))
api.delUser=function(...args){const intent={sequence:++intentSerial,target:args[0]?.id,wire:false};intents.push(intent);return Reflect.apply(originalDelete,this,args).then(object=>{const id=transport.getTransportRequestId(object);if(id)IDs.add(id);replies.push({intent:intent.sequence,target:intent.target,object,id,rejected:false});return object},object=>{const id=transport.getTransportRequestId(object);if(id)IDs.add(id);replies.push({intent:intent.sequence,target:intent.target,object,id,rejected:true});throw object})}
const Page=require(path.join(web,'src/pages/UserList/index.vue')),FailureAlert=require(path.join(web,'src/components/RequestFailureAlert.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(predicate,label){const end=Date.now()+7000;while(Date.now()<end){const value=predicate();if(value)return value;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=window.getComputedStyle(p);if(p.hidden||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
function button(root,label){const list=[...root.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent).replace(/\s/g,'')===label);assert.equal(list.length,1,'one scoped native '+label);return list[0]}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled,'visible enabled native control');n.focus?.();n.click();await settle()}
async function popup(){await until(()=>{const ready=[...document.querySelectorAll('.ant-popconfirm')].filter(n=>visible(n)&&n.textContent.includes('确定删除该用户吗？'));assert.ok(ready.length<=1,'never admit duplicate visible confirmation portals');return ready.length===1},'first actual visible owned confirmation');return[...document.querySelectorAll('.ant-popconfirm')].find(n=>visible(n)&&n.textContent.includes('确定删除该用户吗？'))}
function rowButton(w,target){const row=w.element.querySelector('.desktop-user-table tr[data-row-key="'+target+'"]');assert.ok(row,'actual target row present');return button(row,'删除')}
async function page(){user.setStoredCurrentUser(A);const router=createRouter({history:createMemoryHistory(),routes:[{path:'/users',component:Page}]});await router.push('/users');await router.isReady();const Shell={render:()=>h(Antd.ConfigProvider,{locale:zhCN},{default:()=>h(RouterView)})};const app=mount(Shell,{attachTo:document.body,global:{plugins:[Antd,router],config:{errorHandler:e=>runtime.push(e?.name||'vue-error')}}});const w=app.findComponent(Page);roots.push({app,router,w});await until(()=>!w.vm.tabLoading&&w.vm.dataSource.some(r=>r.id===71)&&w.vm.dataSource.some(r=>r.id===72),'real initial API DTO publishes both native rows');check(app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn','real Chinese Antd provider');return w}
async function dispose(w){const index=roots.findIndex(r=>r.w===w);if(index<0)return;const r=roots.splice(index,1)[0];r.app.unmount();r.router.options.history.destroy();await settle()}
async function dispatch(w,target,spec={}){const start=intents.length;plans.push({target,...spec});await click(rowButton(w,target));await click(button(await popup(),'确定'));await until(()=>intents.length===start+1&&attempts.some(a=>a.intent===intents[start].sequence),'one real delUser/request destructive dispatch');const intent=intents[start];check(attempts.filter(a=>a.intent===intent.sequence).length===1,'one native confirmation issues original single DELETE, never replay');check(!attempts.find(a=>a.intent===intent.sequence).hasBody,'original DELETE remains bodyless');if(!spec.hold)await until(()=>replies.some(r=>r.intent===intent.sequence),'original API terminal object observed');return intent.sequence}
async function release(sequence){const p=pending.find(x=>x.intent===sequence);assert.ok(p&&!p.done,'one actual pending adapter promise');p.finish();await until(()=>replies.some(r=>r.intent===sequence),'actual original deferred API terminal object');await settle();return replies.find(r=>r.intent===sequence)}
function reply(sequence){const r=replies.find(x=>x.intent===sequence);assert.ok(r,'real API terminal object exists');return r}
function panels(w){return[...w.element.querySelectorAll('.request-failure[role="alert"]')].filter(visible)}
function feedback(w,r,label){
 const view=failure.buildRequestFailureView(r.object),components=w.findAllComponents(FailureAlert).filter(c=>toRaw(c.props('failure'))===r.object),owned=components.filter(c=>visible(c.element))
 const scopeRoot=owned[0]?.element,native=[scopeRoot,...(scopeRoot?.querySelectorAll?.('.request-failure[role="alert"]')||[])].filter(n=>n?.matches?.('.request-failure[role="alert"]')&&visible(n)),qualified=owned.length===1&&native.length===1,panel=qualified?native[0]:null
 check(transport.getTransportRequestId(r.object)===r.id&&view.diagnosticId===r.id,label+' actual API object retains existing validated WeakMap ID','transport')
 check(qualified,label+' original response/Error owns one persistent actual RequestFailureAlert','primary')
 check(qualified&&panels(w).length===1,label+' only one owned delete persistent surface','dependent-on-surface',qualified)
 check(qualified&&panel.getAttribute('data-failure-kind')===view.kind,label+' existing classifier kind reused','dependent-on-surface',qualified)
 check(qualified&&compact(panel.textContent).includes(view.title)&&compact(panel.textContent).includes('删除'),label+' safe fixed classifier and delete context visible','dependent-on-surface',qualified)
 const shown=compact(panel?.querySelector('[data-request-diagnostic]')?.textContent),copyButton=panel?.querySelector('[data-action="copy-request-diagnostic"]')||null
 check(qualified&&(r.id?shown===r.id:!shown&&!copyButton),label+' ID display follows validated original metadata only','dependent-on-surface',qualified)
 check(qualified&&!panel.querySelector('[data-action="retry-request"]'),label+' destructive failure has no retry CTA','dependent-on-surface',qualified)
 check(!document.body.textContent.includes(UNSAFE),label+' unsafe response/Error detail not shown anywhere','safe-feedback')
 cur.observations.push({label,intent:r.intent,rejected:r.rejected,diagnosticID:r.id||null,kind:view.kind,originalIdentityOwned:qualified,surfaceCount:owned.length,visibleID:shown||null,rawPrivateTextAbsent:!document.body.textContent.includes(UNSAFE)})
 return{qualified,panel,button:copyButton,reply:r}
}
async function copy(entry,label,mode='resolve'){copyMode=mode;const n=copies.length,np=pendingCopies.length;check(entry.qualified&&entry.button instanceof HTMLButtonElement,label+' actual diagnostic-only native copy exists','dependent-on-surface',entry.qualified);if(entry.qualified&&entry.button instanceof HTMLButtonElement)await click(entry.button);check(copies.length===n+1&&copies[n]?.id===entry.reply.id,label+' writeText contains only same returned ID','dependent-on-surface',entry.qualified);copyMode='resolve';return{qualified:entry.qualified,button:entry.button,pending:mode==='defer'?pendingCopies[np]||null:null}}
async function settleOldCopy(old,label){const n=copies.length;if(old.pending&&!old.pending.done){old.pending.done=true;old.pending.reject(new Error('SYNTHETIC_CLIPBOARD_REFUSED'))}await settle();if(old.button)old.button.click();await settle();check(copies.length===n,label+' retired copy entry cannot start another clipboard write','dependent-on-copy',old.qualified)}
function noDeleteSince(count,label){check(attempts.length===count,label+' no automatic or replayed DELETE')}
async function query(w,kind){const before=calls.length;if(kind==='keyword'){const input=w.element.querySelector('.filter-form input[placeholder="用户名 / 昵称 / 手机号 / 微信"]');assert.ok(input);input.value='SYNTHETIC_CURRENT_QUERY_'+(++querySerial);input.dispatchEvent(new Event('input',{bubbles:true}));await settle();const search=input.closest('.ant-input-search')?.querySelector('.ant-input-search-button');await click(search)}else if(kind==='pagination'){const nextPage=w.vm.pagination.current===1?2:1,control=w.element.querySelector('.desktop-user-table .ant-pagination-item-'+nextPage);assert.ok(control,'actual next pagination control');await click(control)}else await click(button(w.element.querySelector('.section-actions'),'刷新'));await until(()=>!w.vm.tabLoading&&calls.slice(before).some(c=>c.method==='get'&&c.url==='/adapter-service/user'),'explicit current query/list read settles')}
function blockRemoval(){const storage=window.localStorage,p=Object.getPrototypeOf(storage),old=p.removeItem;p.removeItem=function(key){if(this===storage&&key==='adapter_current_user')throw new Error('SYNTHETIC_CACHE_REMOVE_REFUSED');return old.apply(this,arguments)};restoreRemove=()=>{p.removeItem=old}}
function writeLedger(){if(!process.env.USER_DELETE_FAILURE_LEDGER)return;assert.ok(path.isAbsolute(process.env.USER_DELETE_FAILURE_LEDGER));const after=hashes();fs.writeFileSync(process.env.USER_DELETE_FAILURE_LEDGER,JSON.stringify({schema:'USER_DELETE_FAILURE_NATIVE_V1',fixtureSHA256,sourceBefore,sourceAfter:after,sourceStable:JSON.stringify(after)===JSON.stringify(sourceBefore),cases,nativeDOM:true,physicalBrowser:false,realAPIRequestAxiosMemory:true,syntheticOnly:true,noBackendDeleteAuthorizationOrPersistenceClaim:true},null,2)+'\n')}
function register(name,fn){test(name,{timeout:35000},async()=>{cur={name,checks:[],observations:[],passed:false};cases.push(cur);try{await fn();check(realIO.length===0&&runtime.length===0&&consoleErrors.length===0,'no real IO/runtime/console errors');const failed=cur.checks.filter(c=>c.qualified&&!c.passed);assert.deepEqual(failed.map(c=>c.label),[],name+' qualified contract failures');cur.passed=true}catch(e){cur.error={name:e.name,message:String(e.message).replaceAll(UNSAFE,'[PRIVATE_FIXTURE_TEXT]')};throw e}finally{writeLedger()}})}
afterEach(async()=>{try{for(const p of pending)if(!p.done)p.finish();for(const p of pendingCopies)if(!p.done){p.done=true;p.reject(new Error('SYNTHETIC_CLIPBOARD_CLEANUP'))}await settle();if(restoreRemove){restoreRemove();restoreRemove=null}for(const r of roots.splice(0)){r.app.unmount();r.router.options.history.destroy()}await settle();Antd.message.destroy();user.clearStoredCurrentUser();window.localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle();cur.io={calls:[...calls],attempts:attempts.map(a=>({...a})),copies:[...copies],realIO:[...realIO],runtime:[...runtime],consoleErrorCount:consoleErrors.length,consoleWarningCount:consoleWarnings.length};cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(consoleErrors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}finally{writeLedger();calls.length=intents.length=attempts.length=replies.length=plans.length=pending.length=copies.length=pendingCopies.length=realIO.length=runtime.length=consoleErrors.length=consoleWarnings.length=0;IDs.clear();originalErrors.clear();wireResponses.clear();rows=ROWS.map(r=>({...r}));cur=null}})
after(()=>{axios.defaults.adapter=originalAdapter;api.delUser=originalDelete;console.error=oldConsoleError;console.warn=oldConsoleWarn;writeLedger()})

register('UD01 real non200 DELETE payload keeps original WeakMap ID, safe persistent classifier and diagnostic-only copy, isolated from another page instance',async()=>{
 const w=await page(),other=await page(),sequence=await dispatch(w,71),r=reply(sequence),entry=feedback(w,r,'non200 payload')
 check(!r.rejected&&r.object.status===500&&r.object!==wireResponses.get(sequence)&&r.object.data===wireResponses.get(sequence).data&&r.id===attempts.find(a=>a.intent===sequence).id,'actual buildResponsePayload result, not adapter data, owns valid returned ID','transport')
 await copy(entry,'non200 payload');check(panels(other).length===0,'second instance never acquires first instance delete diagnostics','dependent-on-surface',entry.qualified)
 noDeleteSince(1,'reading/copying failure');check(w.vm.dataSource.some(x=>x.id===71),'failed synthetic delete does not pretend row was removed')
})

register('UD02 rejected original Error retains same validated ID; no-response unknown outcome never fabricates a diagnostic or echoes raw private errors',async()=>{
 const w=await page(),first=await dispatch(w,71,{mode:'reject'}),r=reply(first),entry=feedback(w,r,'rejected Error')
 check(r.rejected&&r.object===originalErrors.get(first)&&r.object instanceof Error&&r.object.response?.status===500&&r.id===attempts.find(a=>a.intent===first).id,'real request rejection remains original response-bearing Error with header ID','transport')
 await copy(entry,'rejected Error')
 const second=await dispatch(w,72,{mode:'network',noID:true}),unknown=reply(second),next=feedback(w,unknown,'no-response outcome')
 check(unknown.rejected&&unknown.object.code==='UNKNOWN_OUTCOME'&&!unknown.id&&failure.buildRequestFailureView(unknown.object).kind==='unknown-outcome','original single-write network uncertainty is preserved, no invented ID','transport')
 check(next.qualified&&!next.button,'no-ID failure has no fake copy entry','dependent-on-surface',next.qualified);noDeleteSince(2,'reject/no-ID terminal feedback')
})

register('UD03 only newer valid delete owns failure; new intent retires old copy and late older completion cannot replace current diagnostics',async()=>{
 const w=await page(),first=await dispatch(w,71),initial=feedback(w,reply(first),'initial failure'),old=await copy(initial,'old pending copy','defer')
 const older=await dispatch(w,72,{hold:true});check(panels(w).length===0,'new valid explicit delete clears prior diagnostics','dependent-on-surface',initial.qualified)
 await settleOldCopy(old,'new-delete retirement')
 const newer=await dispatch(w,71,{hold:true});const current=await release(newer),entry=feedback(w,current,'newer failure')
 await release(older);const after=feedback(w,current,'after old completion')
 check(after.qualified&&entry.qualified&&after.reply.object===entry.reply.object,'old already-issued completion cannot replace newer original failure','dependent-on-surface',entry.qualified)
 noDeleteSince(3,'competition and clipboard settlement')
 const oldOwner=await dispatch(w,72,{hold:true});user.setStoredCurrentUser(B);await until(()=>!w.vm.tabLoading&&w.vm.dataSource.length===2,'legitimate B reads finish before old A outcome');await release(oldOwner)
 check(panels(w).length===0,'already-issued A failure cannot attach to new B state','dependent-on-surface',entry.qualified);noDeleteSince(4,'late owner response')
})

register('UD04 explicit keyword, pagination and list refresh retire received diagnostics and pending old-query failures without automatic writes',async()=>{
 for(const kind of ['keyword','pagination','refresh']){
  const w=await page(),first=await dispatch(w,71),entry=feedback(w,reply(first),kind+' received failure'),old=await copy(entry,kind+' pending copy','defer'),n=attempts.length
  await query(w,kind);check(panels(w).length===0,kind+' current read retires received delete diagnostics','dependent-on-surface',entry.qualified);await settleOldCopy(old,kind+' query retirement');noDeleteSince(n,kind+' explicit read')
  const held=await dispatch(w,72,{hold:true});await query(w,kind);await release(held);check(panels(w).length===0,kind+' late prior-context failure cannot reattach','dependent-on-surface',entry.qualified);noDeleteSince(n+1,kind+' late response');await dispose(w)
 }
})

register('UD05 actor, role, cached-logout and unmount retire visible diagnostic/copy entries without introducing a fresh manual-action fence',async()=>{
 for(const transition of ['actor','role','cached-logout','unmount']){
  const w=await page(),sequence=await dispatch(w,71),entry=feedback(w,reply(sequence),transition+' received failure'),old=await copy(entry,transition+' pending copy','defer'),n=attempts.length
  if(transition==='actor')user.setStoredCurrentUser(B)
  else if(transition==='role')user.setStoredCurrentUser({...A,role:'user'})
  else if(transition==='cached-logout'){const epoch=user.getCurrentUserLogoutEpoch();blockRemoval();user.clearStoredCurrentUser();check(user.getCurrentUserLogoutEpoch()!==epoch&&user.getStoredCurrentUser()?.id===A.id,'real cached-removal denial still advances explicit logout epoch')}
  else await dispose(w)
  const copied=copies.length;if(old.button)old.button.click();check(copies.length===copied,transition+' immediately retired old copy cannot dispatch','dependent-on-copy',entry.qualified);await settle()
  if(transition!=='unmount')check(!w.element.querySelector('.request-failure'),transition+' clears old diagnostic DOM after normal Vue rendering','dependent-on-surface',entry.qualified)
  await settleOldCopy(old,transition+' owner retirement');noDeleteSince(n,transition+' retirement')
  if(transition==='actor')await until(()=>!w.vm.tabLoading&&w.vm.dataSource.length===2,'legitimate new actor automatic reload, not old delete IO')
  if(restoreRemove){restoreRemove();restoreRemove=null}await dispose(w);Antd.message.destroy()
 }
})

register('UD06 original native confirmation cancel, row-presence guard, one successful bodyless DELETE and original single readback remain unchanged',async()=>{
 const w=await page(),reads=calls.filter(c=>c.method==='get'&&c.url==='/adapter-service/user').length
 await click(rowButton(w,71));await click(button(await popup(),'取消'));check(attempts.length===0,'actual confirmation Cancel causes no DELETE')
 w.vm.onDelete(999999);await settle();check(attempts.length===0,'original absent-row guard causes no DELETE')
 const sequence=await dispatch(w,71,{status:200});await until(()=>!w.vm.tabLoading&&!w.vm.dataSource.some(r=>r.id===71),'successful original list readback')
 check(reply(sequence).object.status===200&&attempts.length===1&&!attempts[0].hasBody,'one confirmed original bodyless successful DELETE')
 check(calls.filter(c=>c.method==='get'&&c.url==='/adapter-service/user').length===reads+1,'200 preserves exactly original one list readback')
 check(!w.element.querySelector('tr[data-row-key="71"]')&&w.vm.dataSource.some(r=>r.id===72)&&panels(w).length===0,'successful current DOM removes only synthetic target and has no failure surface')
})


// Auth-local supplement only: run ^UD0[78] ; original six cases above remain byte-exact.
register('UD07 actual403 retains original ID and copy with fixed safe feedback, no unsafe global toast and no logout',async()=>{
 const w=await page(),epoch=user.getCurrentUserLogoutEpoch(),sequence=await dispatch(w,71,{status:403}),r=reply(sequence),entry=feedback(w,r,'actual403 local feedback')
 check(!r.rejected&&r.object.status===403&&r.object!==wireResponses.get(sequence)&&r.object.data===wireResponses.get(sequence).data&&r.id===attempts.find(a=>a.intent===sequence).id,'actual403 original buildResponsePayload retains validated header ID','transport')
 check(failure.buildRequestFailureView(r.object).kind==='forbidden','actual403 original classifier remains forbidden','transport')
 check(user.getStoredCurrentUser()?.id===A.id&&user.getCurrentUserLogoutEpoch()===epoch,'actual403 preserves current principal and logout epoch','auth-positive-control')
 check(!document.body.textContent.includes(UNSAFE),'actual403 does not leak private server detail through shared global toast','primary-auth-local')
 await copy(entry,'actual403 diagnostic copy')
 check(w.vm.dataSource.some(x=>x.id===71)&&w.vm.canManageUsers,'actual403 does not pretend row removal or revoke current admin display','auth-positive-control')
 noDeleteSince(1,'actual403 display and copy')
})

register('UD08 current401 preserves original cleanup and logout epoch while late oldA401 cannot clear B or attach old diagnostics',async()=>{
 window.history.replaceState(null,'','/login')
 check(window.location.pathname==='/login','synthetic document is already at original login target; no physical navigation claim','fixture-setup')
 const w=await page(),epochA=user.getCurrentUserLogoutEpoch(),current=await dispatch(w,71,{status:401}),r=reply(current)
 await settle()
 check(!r.rejected&&r.object.status===401&&r.id===attempts.find(a=>a.intent===current).id&&transport.getTransportRequestId(r.object)===r.id,'current401 original payload and validated ID still delivered','transport')
 check(!user.getStoredCurrentUser()?.id&&user.getCurrentUserLogoutEpoch()!==epochA,'current401 still performs original current-user cleanup and epoch advance','primary-auth-positive')
 check(w.vm.dataSource.length===0&&!w.vm.canManageUsers&&panels(w).length===0,'current401 retires old rows and delete feedback instead of retaining private old entry','auth-positive-control')
 check(window.location.pathname==='/login','original login target remains selected without unsupported JSDOM navigation','fixture-setup')
 check(!document.body.textContent.includes(UNSAFE),'current401 never shows raw private server detail','auth-positive-control')
 noDeleteSince(1,'current401 cleanup')
 await dispose(w);Antd.message.destroy()
 const next=await page(),old=await dispatch(next,72,{status:401,hold:true})
 user.setStoredCurrentUser(B)
 await until(()=>!next.vm.tabLoading&&next.vm.dataSource.length===2&&user.getStoredCurrentUser()?.id===B.id,'legitimate current B automatic reads finish before old A401')
 const epochB=user.getCurrentUserLogoutEpoch(),oldReply=await release(old)
 check(!oldReply.rejected&&oldReply.object.status===401&&transport.getTransportRequestId(oldReply.object)===oldReply.id,'late oldA401 still delivers its original correlated payload','transport')
 check(user.getStoredCurrentUser()?.id===B.id&&user.getCurrentUserLogoutEpoch()===epochB,'late oldA401 cannot clear B or advance B logout epoch','primary-auth-positive')
 check(next.vm.canManageUsers&&next.vm.dataSource.length===2&&panels(next).length===0&&!document.body.textContent.includes(oldReply.id),'late oldA401 cannot attach old diagnostics or erase B rows','auth-positive-control')
 check(!document.body.textContent.includes(UNSAFE),'late oldA401 produces no unsafe global or page feedback','auth-positive-control')
 noDeleteSince(2,'late old401 delivery')
})
