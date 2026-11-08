/* Actual App/Antd/BillingRecordList caller and shared BillingDetailDialog with real request and strict inert transport.
 * Strict owned Modal contract; observations retained. Synthetic self-owned normal_search row only; no business writes, locations, copy or physical Tab. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}};globalThis.getComputedStyle=window.getComputedStyle
for(const k of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','HTMLTextAreaElement','CustomEvent'])globalThis[k]=window[k]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick}=require('vue'),{createRouter,createMemoryHistory}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const user=require(path.join(web,'src/utils/currentUser.js'))
const ACTOR={id:77931,role:'user',username:'SYNTHETIC_BILLING_MODAL_USER',status:'enabled',balance:3.234567}
const ROW={id:8091,userId:77931,username:'SYNTHETIC_BILLING_MODAL_USER',type:'normal_search',direction:'expense',subject:'Synthetic readonly billing modal fixture',question:'Synthetic readonly billing modal fixture',provider:'synthetic',requestId:'SYNTHETIC-BILLING-8091',amount:0.000062,createdAt:'2026-11-02T00:00:00Z',showInStatement:true,clientIp:'',accessTokenName:'',unitPriceSnapshot:0.000062,cacheHitRate:null}
const cases=[],roots=[],calls=[],realIO=[],runtime=[],errors=[],warnings=[];let cur
const compact=x=>String(x??'').replace(/\s+/g,'').trim(),H=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')
const inputs=['src/App.vue','src/pages/BillingRecordList/index.vue','src/components/BillingDetailDialog.vue','src/utils/userPortalHelpers.mjs','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/legacyFormAccessibility.mjs','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputs.map(p=>[p,H(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=H(__filename)
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_SHARED_BILLING_MODAL_IO_'+kind)}
const originalAdapter=axios.defaults.adapter
assert.equal(!!require.cache[path.join(web,'src/utils/request.js')],false,'inert adapter before actual request/API import')
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url),paramsKeys=Object.keys(config.params||{}).sort();calls.push({method,url,paramsKeys})
 if(method!=='get')return Promise.reject(deny('nonGET'))
 let data
 if(url==='/adapter-service/public/register-status')data={siteName:'Synthetic Billing App'}
 else if(url==='/adapter-service/billing-records'){if(JSON.stringify(config.params)!==JSON.stringify({page:1,pageSize:10}))return Promise.reject(deny('unexpected-billing-params'));data={data:[{...ROW}],total:1,page:1,pageSize:10,summary:{totalAmount:0.000062,currentPageAmount:0.000062,currentPageCacheHitRate:null,totalCacheHitRate:null}}}
 else return Promise.reject(deny('unknownGET'))
 return Promise.resolve({status:200,data,headers:{},config,request:{memoryOnly:true}})
}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
for(const n of new Set([window.navigator,globalThis.navigator].filter(Boolean)))Object.defineProperty(n,'clipboard',{configurable:true,value:{writeText:()=>deny('clipboard-write'),readText:()=>deny('clipboard-read')}})
document.execCommand=()=>deny('clipboard-command')
window.addEventListener('error',e=>runtime.push(e.error?.name||'window-error'));window.addEventListener('unhandledrejection',e=>runtime.push(e.reason?.name||'unhandled-rejection'))
const oldError=console.error,oldWarn=console.warn
console.error=(...a)=>{errors.push({argumentCount:a.length});return Reflect.apply(oldError,console,a)};console.warn=(...a)=>{warnings.push({argumentCount:a.length});return Reflect.apply(oldWarn,console,a)}
const Swal=require('sweetalert2'),oldSwalFire=Swal.fire;Swal.fire=()=>deny('swal-popup')
const App=require(path.join(web,'src/App.vue')),Page=require(path.join(web,'src/pages/BillingRecordList/index.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(fn,label){const end=Date.now()+7000;while(Date.now()<end){const value=fn();if(value)return value;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=getComputedStyle(p);if(p.hidden||p.inert||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
function button(scope,title){assert.ok(!['确定','删除','复制','显示全部','显示全部令牌'].includes(title),'never admits destructive/reveal/copy control');assert.ok(scope);const a=[...scope.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent)===title);assert.equal(a.length,1,'one original native '+title);return a[0]}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled);n.focus();n.click();await settle()}
function entering(n){return /-(?:enter|appear)-(?:active|from|prepare|start)\b/.test(String(n?.className||''))}
function focus(opener,dialog){const a=document.activeElement,role=dialog?.closest('[role="dialog"]')||dialog?.querySelector('[role="dialog"]');return{tag:a?.tagName||null,isBody:a===document.body,isOpener:a===opener,insideTarget:!!role?.contains(a)}}
function observe(label,e){
 const dialogs=[...document.querySelectorAll('[role="dialog"]')].filter(visible).map(role=>{
  const ids=String(role.getAttribute('aria-labelledby')||'').split(/\s+/).filter(Boolean),resolved=compact(ids.map(id=>document.getElementById(id)?.textContent||'').join(' ')),title=compact(role.querySelector('.ant-modal-title')?.textContent),allowed=['账单详情']
  return{role:role.getAttribute('role'),title:allowed.includes(title)?title:'[UNEXPECTED_TITLE]',ariaModal:role.getAttribute('aria-modal'),labelledByCount:ids.length,resolvedName:allowed.includes(resolved)?resolved:'[UNEXPECTED_OR_MISSING_NAME]',activeInside:role.contains(document.activeElement)}
 })
 cur.observations.push({label,title:e.title,dialogs,entering:entering(e.dialog),focus:focus(e.opener,e.dialog),timing:label==='admitted'?'after native click plus ordinary helper settle; NOT immediate':'natural lifecycle readback',syntheticReadonlyRecord:true})
}
async function appPage(){
 user.setStoredCurrentUser(ACTOR)
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/billing-records',component:Page}]});await router.push('/adapter/billing-records');await router.isReady()
 const app=mount(App,{attachTo:document.body,global:{plugins:[Antd,router],stubs:{transition:false,'transition-group':false},config:{errorHandler:e=>runtime.push(e?.name||'vue-error')}}});roots.push({app,router});await settle()
 const w=app.findComponent(Page);assert.ok(w.exists(),'actual BillingRecordList through current App')
 await until(()=>!w.vm.tableLoading&&w.vm.dataSource.some(r=>r.id===8091),'actual raw billing DTO becomes native row')
 cur.checks.push({label:'actual App zhCN plus lawful ordinary self-owner admission',passed:app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn'&&w.vm.canLoadForCurrentUser()&&!w.vm.isAdmin})
 return w
}
async function open(w){
 const title='账单详情',opener=button(w.element.querySelector('tr[data-row-key="8091"]'),'查看详情');await click(opener)
 const dialog=await until(()=>[...document.querySelectorAll('.ant-modal')].find(n=>visible(n)&&compact(n.querySelector('.ant-modal-title')?.textContent)===title),'actual shared BillingDetailDialog'),e={w,title,opener,dialog};observe('admitted',e)
 await until(()=>!entering(dialog),'original real Transition naturally settles');await settle();observe('settled',e)
 const role=dialog.closest('[role="dialog"]')||dialog.querySelector('[role="dialog"]'),ids=String(role?.getAttribute('aria-labelledby')||'').split(/\s+/).filter(Boolean),name=compact(ids.map(id=>document.getElementById(id)?.textContent||'').join(' '))
 cur.checks.push({label:'active shared billing Modal declares aria-modal true',category:'primary',qualified:!!role,passed:role?.getAttribute('aria-modal')==='true'})
 cur.checks.push({label:'one actual native role with resolved billing-detail name',category:'positive',qualified:true,passed:!!role&&name==='账单详情'&&[...document.querySelectorAll('[role="dialog"]')].filter(visible).length===1})
 cur.checks.push({label:'naturally settled owned dialog focus is internal',category:'positive',qualified:true,passed:!!role?.contains(document.activeElement)})
 cur.checks.push({label:'ordinary self detail exposes no statement write switch',category:'positive',qualified:true,passed:dialog.querySelectorAll('[role="switch"]').length===0})
 cur.observations.push({label:'self-view readonly controls',statementSwitchCount:dialog.querySelectorAll('[role="switch"]').length,detailRootPresent:!!dialog.querySelector('.billing-detail'),sharedOpenProp:w.findComponent({name:'BillingDetailDialog'}).props('open')===true})
 return e
}
async function close(e){
 const footerClose=button(e.dialog.querySelector('.ant-modal-footer'),'关闭');await click(footerClose);await until(()=>!e.w.vm.billingDetailOpen&&!visible(e.dialog),'original footer close hides owned shared Modal');await settle()
 cur.observations.push({label:'closed-settled',title:e.title,oldPanelConnected:e.dialog.isConnected,oldPanelVisible:visible(e.dialog),detailContentStillConnected:!!e.dialog.querySelector('.billing-detail')?.isConnected,focus:focus(e.opener,e.dialog),visibleDialogCount:[...document.querySelectorAll('[role="dialog"]')].filter(visible).length,restorePolicy:'production custom manual-owned restore; vendor auto-return disabled; observation only'})
 cur.checks.push({label:'original custom owned restore returns exact opener',category:'positive',qualified:true,passed:document.activeElement===e.opener})
 cur.checks.push({label:'destroy-on-close disconnects old panel and detail',category:'positive',qualified:true,passed:!e.dialog.isConnected&&!e.dialog.querySelector('.billing-detail')?.isConnected})
 cur.checks.push({label:'original close leaves no visible dialog and closed parent state',category:'positive',qualified:true,passed:!e.w.vm.billingDetailOpen&&[...document.querySelectorAll('[role="dialog"]')].filter(visible).length===0})
}
function write(){if(process.env.SHARED_BILLING_MODAL_CONTRACT_LEDGER)fs.writeFileSync(process.env.SHARED_BILLING_MODAL_CONTRACT_LEDGER,JSON.stringify({schema:'ACTUAL_APP_SHARED_BILLING_MODAL_CONTRACT_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),sourceStable:JSON.stringify(sourceBefore)===JSON.stringify(hashes()),cases,actualApp:true,legacyFallback:true,observationNotSemanticAcceptance:false,physicalTab:{qualified:false,reason:'jsdom focus/dispatch is not genuine keyboard Tab; Root Cua owns it'},physicalEscape:{qualified:false,reason:'no genuine Escape action; Root Cua owns it'},syntheticReadonlyRecord:true,noClipboard:true},null,2)+'\n')}
test('BD01 actual App shared BillingDetailDialog modal declaration preserves custom restore and readonly caller',{timeout:30000},async()=>{
 cur={name:'BD01',completedFlow:false,passed:false,checks:[],observations:[]};cases.push(cur)
 try{
  const w=await appPage(),initial=calls.length,first=await open(w);await close(first);const second=await open(w);cur.observations.push({label:'destroy-on-close reopened panel identity',sameNode:first.dialog===second.dialog});cur.checks.push({label:'reopening creates the original fresh destroyed panel',category:'positive',qualified:true,passed:first.dialog!==second.dialog});await close(second)
  cur.checks.push({label:'two native detail opens and closes add zero API reads or business writes',passed:calls.length===initial&&calls.every(c=>c.method==='get')})
  cur.checks.push({label:'exact initial App publicconfig and billing read allowlist',passed:calls.length===2&&calls.some(c=>c.url==='/adapter-service/public/register-status')&&calls.some(c=>c.url==='/adapter-service/billing-records')})
  cur.checks.push({label:'zero clipboard/real IO/runtime/console errors',passed:realIO.length===0&&runtime.length===0&&errors.length===0})
  cur.completedFlow=true;assert.deepEqual(cur.checks.filter(c=>c.qualified!==false&&!c.passed).map(c=>c.label),[],'strict shared Modal contract, all complete-tail checks');cur.passed=true
 }finally{write()}
})
afterEach(async()=>{try{for(const r of roots.splice(0)){r.app.unmount();r.router.options.history.destroy()}await settle();Antd.Modal.destroyAll();Antd.message.destroy();user.clearStoredCurrentUser();localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle();cur.io={calls:[...calls],writes:calls.filter(c=>c.method!=='get').length,realIO:[...realIO],runtimeCount:runtime.length,consoleErrorCount:errors.length,consoleWarningCount:warnings.length};cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(errors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}finally{write()}})
after(()=>{Swal.fire=oldSwalFire;axios.defaults.adapter=originalAdapter;console.error=oldError;console.warn=oldWarn;write()})
