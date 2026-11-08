/* Actual UserList + Antd zhCN + real API/request/Axios, inert allowlisted transport only.
 * Client guards/owned confirmation qualification, not server authorization or physical-browser proof. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
assert.ok(path.isAbsolute(web),'absolute source web required')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}}
globalThis.getComputedStyle=window.getComputedStyle
for(const key of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','CustomEvent'])globalThis[key]=window[key]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,h}=require('vue'),{createRouter,createMemoryHistory,RouterView}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const zhCN=require('ant-design-vue/lib/locale/zh_CN').default,user=require(path.join(web,'src/utils/currentUser.js'))
const A={id:65001,username:'SYNTHETIC_MANAGEMENT_ADMIN_A',role:'admin',status:'enabled'},B={id:65002,username:'SYNTHETIC_MANAGEMENT_ADMIN_B',role:'admin',status:'enabled'},ordinary={id:65003,username:'SYNTHETIC_MANAGEMENT_USER',role:'user',status:'enabled'},TARGET_ID=71
const cases=[],calls=[],roots=[],realIO=[],runtime=[],consoleErrors=[],consoleWarnings=[];let cur=null,deleted=false,restoreRemove=null
const sha=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),compact=v=>String(v??'').replace(/\s+/g,' ').trim(),clone=v=>v===undefined?undefined:JSON.parse(JSON.stringify(v))
const inputNames=['src/pages/UserList/index.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/requestHelpers.mjs','src/utils/requestRetry.mjs','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/userPortalHelpers.mjs','src/utils/listSearchHelpers.mjs','src/utils/moneyFormat.mjs','src/utils/locationHelpers.mjs','src/composables/useScopedSessionPreferences.mjs','src/composables/useTableColumnPreferences.mjs','src/components/RequestFailureAlert.vue','src/components/TableColumnSettings.vue','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputNames.map(p=>[p,sha(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=sha(__filename)
const check=(value,label)=>cur.checks.push({label,passed:!!value})
function row(actor){return{id:TARGET_ID,username:'SYNTHETIC_MEMBER_'+(actor?.id===B.id?'B':'A'),nickname:'合成用户',role:'user',status:'enabled',balance:1.234567,phone:'',wechatId:'',perms:'search',registerSource:'admin',lastLoginIp:'',registerIp:''}}
const originalAdapter=axios.defaults.adapter
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url),actor=user.getStoredCurrentUser(),call={method,url,actor:actor?.id||null,role:actor?.role||null,params:clone(config.params||{})};calls.push(call)
 const allowed=method==='get'&&['/adapter-service/user','/adapter-service/system-settings'].includes(url)||method==='delete'&&url==='/adapter-service/user/'+TARGET_ID
 if(!allowed){realIO.push('unexpected-api-operation');return Promise.reject(new Error('FORBIDDEN_API_OPERATION'))}
 let body
 if(method==='delete'){call.hasRequestBody=config.data!==undefined&&config.data!==null&&config.data!=='';deleted=true;body={message:'删除用户成功'}}
 else if(url==='/adapter-service/system-settings')body={newUserBonusAmount:.123456}
 else body={data:deleted?[]:[row(actor)],total:deleted?0:1,page:Number(config.params?.page)||1,pageSize:Number(config.params?.pageSize)||10}
 return Promise.resolve({status:200,data:body,headers:{'x-request-id':calls.length.toString(16).padStart(32,'0')},config,request:{memoryOnly:true}})
}
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_REAL_IO_'+kind)}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(String(e.error?.stack||e.message)));window.addEventListener('unhandledrejection',e=>runtime.push(String(e.reason?.stack||e.reason)))
const originalConsoleError=console.error,originalConsoleWarn=console.warn
console.error=function(...args){consoleErrors.push(args.map(x=>String(x?.stack||x)).join(' '));return Reflect.apply(originalConsoleError,this,args)}
console.warn=function(...args){consoleWarnings.push(args.map(x=>String(x?.stack||x)).join(' '));return Reflect.apply(originalConsoleWarn,this,args)}
const Page=require(path.join(web,'src/pages/UserList/index.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(predicate,label){const deadline=Date.now()+7000;while(Date.now()<deadline){if(predicate())return;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=window.getComputedStyle(p);if(p.hidden||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled,'actual visible enabled native click');n.focus?.();n.click();await settle()}
function button(root,label){const a=[...root.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent).replace(/\s/g,'')===label);assert.equal(a.length,1,'one scoped native '+label+' button');return a[0]}
function nativeDelete(w){const row=w.element.querySelector('.desktop-user-table tr[data-row-key="'+TARGET_ID+'"]');assert.ok(row,'actual current target row');return button(row,'删除')}
async function popup(){await until(()=>{const ready=[...document.querySelectorAll('.ant-popconfirm')].filter(n=>visible(n)&&n.textContent.includes('确定删除该用户吗？'));assert.ok(ready.length<=1,'no duplicate visible owned Popconfirm portal during admission');return ready.length===1},'first actual visible owned Popconfirm portal');const a=[...document.querySelectorAll('.ant-popconfirm')].filter(n=>visible(n)&&n.textContent.includes('确定删除该用户吗？'));assert.equal(a.length,1,'one actual visible owned Popconfirm portal');return a[0]}
function captureConfirm(w,trigger){const c=w.findAllComponents(Antd.Popconfirm).find(c=>c.element===trigger||c.element.contains(trigger));assert.ok(c,'actual Popconfirm that owns desktop trigger');const fn=c.vm.$.vnode.props?.onConfirm;assert.equal(typeof fn,'function','original captured onConfirm closure');return fn}
function view(w){return{ids:w.vm.dataSource.map(r=>r.id),rowMarkers:w.vm.dataSource.map(r=>r.username),loading:w.vm.tabLoading,total:w.vm.pagination.total,canManageDisplay:w.vm.canManageUsers,liveOwnerAllowed:w.vm.userListContext.owner.capture().isAlive(),editorOpen:w.vm.visible,passwordOpen:w.vm.resetPasswordVisible,desktopRows:[...w.element.querySelectorAll('.desktop-user-table tr[data-row-key]')].map(n=>n.getAttribute('data-row-key')),hasPermissionWarning:w.element.textContent.includes('当前账号无用户管理权限。')}}
async function open(actor=A){
 user.setStoredCurrentUser(actor)
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/users',component:Page}]});await router.push('/adapter/users');await router.isReady()
 const Shell={render:()=>h(Antd.ConfigProvider,{locale:zhCN},{default:()=>h(RouterView)})},app=mount(Shell,{attachTo:document.body,global:{plugins:[Antd,router],config:{errorHandler:e=>runtime.push(String(e?.stack||e))}}});roots.push({app,router});await settle()
 const w=app.findComponent(Page);assert.ok(w.exists(),'actual UserList via RouterView, no fake permission wrapper')
 check(app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn','actual zhCN provider')
 if(actor.role==='admin'){
  await until(()=>!w.vm.tabLoading&&w.vm.dataSource.some(r=>r.id===TARGET_ID),'valid actual admin list response publishes')
  check(calls.filter(c=>c.url==='/adapter-service/user'&&c.method==='get').length>=1&&calls.some(c=>c.url==='/adapter-service/system-settings'&&c.method==='get'),'actual API/request makes only original user-list and synthetic bonus-default reads')
  check(w.vm.dataSource.length===1&&w.vm.pagination.total===1&&!!w.element.querySelector('tr[data-row-key="'+TARGET_ID+'"]')&&w.element.textContent.includes(row(actor).username),'valid public DTO reaches actual native user row and total')
 }
 return w
}
function writeLedger(){if(process.env.USER_MANAGEMENT_LEDGER)fs.writeFileSync(process.env.USER_MANAGEMENT_LEDGER,JSON.stringify({schema:'USER_MANAGEMENT_NATIVE_LEDGER_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),cases,nativeDOM:true,physicalBrowser:false,actualPageAPIRequestAxiosMemory:true,noPasswordFullFormOrPrivateProviderDataRecorded:true,backendAuthorizationNotQualified:true},null,2)+'\n')}
function register(name,fn){test(name,{timeout:20000},async()=>{cur={name,checks:[],observations:[],passed:false};cases.push(cur);try{await fn();check(realIO.length===0&&runtime.length===0&&consoleErrors.length===0,'zero real IO/runtime/console errors');const failed=cur.checks.filter(c=>!c.passed);assert.equal(failed.length,0,failed.map(c=>c.label).join('; '));cur.passed=true}catch(error){cur.error={name:error.name,message:String(error.message)};throw error}finally{writeLedger()}})}
afterEach(async()=>{if(!cur)return;try{if(restoreRemove){restoreRemove();restoreRemove=null}for(const{app,router}of roots.splice(0)){app.unmount();router.options.history.destroy()}await settle();Antd.message.destroy();user.clearStoredCurrentUser();window.localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle();cur.io={calls:[...calls],GETs:calls.filter(c=>c.method==='get').length,DELETEs:calls.filter(c=>c.method==='delete').length,realIO:[...realIO],runtime:[...runtime],consoleErrors:[...consoleErrors],consoleWarnings:[...consoleWarnings]};cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(consoleErrors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}catch(error){cur.passed=false;cur.hookError={name:error.name,message:String(error.message)};throw error}finally{writeLedger();calls.length=realIO.length=runtime.length=consoleErrors.length=consoleWarnings.length=0;deleted=false;cur=null}})
after(()=>{axios.defaults.adapter=originalAdapter;console.error=originalConsoleError;console.warn=originalConsoleWarn;writeLedger()})

register('UM01 ordinary real UserList mount and explicit denied native actions perform zero private reads or destructive writes',async()=>{
 const w=await open(ordinary),header=w.element.querySelector('.section-actions');assert.ok(header)
 check(!w.vm.canManageUsers&&w.element.textContent.includes('当前账号无用户管理权限。')&&w.vm.dataSource.length===0,'ordinary account has actual fixed permission warning and no private rows')
 check(calls.length===0,'ordinary initial mount emits zero user-list/bonus private GETs')
 await click(button(header,'刷新'));await click(button(header,'新增用户'));w.vm.deleteConfirmation(TARGET_ID)();w.vm.onDelete(TARGET_ID);await settle()
 check(calls.length===0&&!w.vm.visible&&!w.vm.resetPasswordVisible,'original denied native actions and guarded delete entries issue no GET/DELETE and open no private editor')
 check(!w.element.querySelector('.user-operation-actions')&&!document.querySelector('.ant-popconfirm'),'ordinary page exposes no current row delete confirmation')
 cur.observations.push({label:'ordinary client guard only, not backend authorization',state:view(w)})
})

register('UM02 administrator valid read and actual Popconfirm cancel preserve zero writes; one explicit native confirmation issues only original bodyless DELETE then original readback',async()=>{
 const w=await open(),initial=calls.length,trigger=nativeDelete(w);await click(trigger);let p=await popup()
 check(calls.length===initial&&calls.filter(c=>c.method==='delete').length===0,'opening native dangerous-action confirmation does not write or read')
 await click(button(p,'取消'))
 check(calls.length===initial&&w.vm.dataSource.some(r=>r.id===TARGET_ID),'actual native Cancel keeps target row and causes zero DELETE/GET')
 await click(nativeDelete(w));p=await popup();await click(button(p,'确定'));await until(()=>!w.vm.tabLoading&&w.vm.dataSource.length===0&&calls.some(c=>c.method==='delete'),'confirmed delete and original followup GET finish')
 const writes=calls.filter(c=>c.method==='delete'),reads=calls.filter(c=>c.method==='get'&&c.url==='/adapter-service/user')
 check(writes.length===1&&writes[0].url==='/adapter-service/user/'+TARGET_ID&&writes[0].actor===A.id&&!writes[0].hasRequestBody,'one explicit confirmation dispatches original single owned bodyless DELETE at selected target id')
 check(reads.length===2&&calls.filter(c=>c.url==='/adapter-service/system-settings').length===1&&w.vm.pagination.total===0&&w.vm.dataSource.length===0,'successful deletion preserves only original one user-list readback and valid empty total, not bonus/config mutation')
 check(w.element.textContent.includes('当前还没有用户记录')&&!w.element.querySelector('tr[data-row-key="'+TARGET_ID+'"]'),'actual current native DOM removes target after successful readback')
 cur.observations.push({label:'explicit delete original workflow, synthetic server only',state:view(w),writeCount:writes.length})
})

register('UM03 captured old confirmation and old native portal cannot dispatch after actor/role/cached-logout retirement; normal B reload is not old A IO',async()=>{
 for(const transition of ['actor','role','cached-logout']){
  const start=calls.length,w=await open(),trigger=nativeDelete(w),oldCallback=captureConfirm(w,trigger);await click(trigger);const oldPortalButton=button(await popup(),'确定'),before=calls.length,epoch=user.getCurrentUserLogoutEpoch()
  if(transition==='actor')user.setStoredCurrentUser(B)
  else if(transition==='role')user.setStoredCurrentUser({...A,role:'user'})
  else{const storage=window.localStorage,proto=Object.getPrototypeOf(storage),remove=proto.removeItem;proto.removeItem=function(key){if(this===storage&&key==='adapter_current_user')throw new Error('SYNTHETIC_REMOVE_REFUSED');return remove.apply(this,arguments)};restoreRemove=()=>{proto.removeItem=remove};user.clearStoredCurrentUser();check(user.getCurrentUserLogoutEpoch()!==epoch&&user.getStoredCurrentUser()?.id===A.id,'actual cached logout advances epoch while cache removal genuinely fails')}
  check(w.vm.dataSource.length===0,'principal retirement synchronously clears old row state: '+transition)
  oldPortalButton.click();oldCallback()
  check(calls.length===before&&calls.slice(start).every(c=>c.method!=='delete'),'same-tick old portal/captured confirmation cannot issue private IO: '+transition)
  await settle()
  if(transition==='actor')await until(()=>!w.vm.tabLoading&&w.vm.dataSource.some(r=>r.username==='SYNTHETIC_MEMBER_B'),'normal current B automatic read publishes')
  const currentCalls=calls.length,extra=calls.slice(before)
  check(transition==='actor'?extra.length===2&&extra.every(c=>c.method==='get'&&c.actor===B.id):extra.length===0,'only legitimate new B user/default GETs allowed; role/logout retirement emits no private reads: '+transition)
  oldCallback();oldPortalButton.click();await settle()
  check(calls.length===currentCalls&&calls.slice(start).every(c=>c.method!=='delete'),'after-tick retired confirmation stays inert without replay or writes: '+transition)
  check(transition==='actor'?w.vm.dataSource.some(r=>r.username==='SYNTHETIC_MEMBER_B'):w.vm.dataSource.length===0,'old confirm cannot erase current B rows or restore retired data: '+transition)
  cur.observations.push({label:'retired original confirmation '+transition,state:view(w),wireBeforeTransition:before,legitimateNewActorGETs:extra.map(c=>({url:c.url,actor:c.actor})),oldCapturedConfirmationNoNewIO:calls.length===currentCalls})
  if(restoreRemove){restoreRemove();restoreRemove=null}
  roots.pop().app.unmount();await settle();user.clearStoredCurrentUser();window.localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle()
 }
 cur.manualBoundary='Existing99 boundary preserved: only original captured confirmation/portal retirement. Do not infer a blanket ban on newly captured manual actions from residual cached identity; no cross-site mount-fence migration.'
})
