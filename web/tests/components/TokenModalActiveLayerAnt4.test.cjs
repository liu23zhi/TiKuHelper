/* Actual App/Antd/UserToken page with real request and strict inert transport.
 * Observation only. No secret token, write, reveal/copy, Swal or physical Tab. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}};globalThis.getComputedStyle=window.getComputedStyle
for(const k of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','HTMLTextAreaElement','CustomEvent'])globalThis[k]=window[k]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick}=require('vue'),{createRouter,createMemoryHistory}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const user=require(path.join(web,'src/utils/currentUser.js'))
const ACTOR={id:77921,role:'user',username:'SYNTHETIC_TOKEN_MODAL_USER',status:'enabled',balance:3.234567}
const ROW={id:72001,name:'SYNTHETIC_TOKEN_ROW',tokenValue:'',status:'enabled',totalQuotaLimit:.000062,totalAmountUsed:1.234567,remark:'synthetic-only'}
const cases=[],roots=[],calls=[],realIO=[],runtime=[],errors=[],warnings=[];let cur
const compact=x=>String(x??'').replace(/\s+/g,'').trim(),H=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')
const inputs=['src/App.vue','src/pages/AccessTokenManagement/index.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/legacyFormAccessibility.mjs','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputs.map(p=>[p,H(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=H(__filename)
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_TOKEN_MODAL_IO_'+kind)}
const originalAdapter=axios.defaults.adapter
assert.equal(!!require.cache[path.join(web,'src/utils/request.js')],false,'inert adapter before actual request/API import')
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url),paramsKeys=Object.keys(config.params||{}).sort();calls.push({method,url,paramsKeys})
 if(method!=='get')return Promise.reject(deny('nonGET'))
 let data
 if(url==='/adapter-service/public/register-status')data={siteName:'Synthetic Token App'}
 else if(url==='/adapter-service/access-tokens'){if(paramsKeys.length)return Promise.reject(deny('unexpected-token-params'));data=[{...ROW}]}
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
const App=require(path.join(web,'src/App.vue')),Page=require(path.join(web,'src/pages/AccessTokenManagement/index.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(fn,label){const end=Date.now()+7000;while(Date.now()<end){const value=fn();if(value)return value;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=getComputedStyle(p);if(p.hidden||p.inert||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
function button(scope,title){assert.ok(!['确定','删除','复制','显示全部','显示全部令牌'].includes(title),'never admits destructive/reveal/copy control');assert.ok(scope);const a=[...scope.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent)===title);assert.equal(a.length,1,'one original native '+title);return a[0]}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled);n.focus();n.click();await settle()}
function entering(n){return /-(?:enter|appear)-(?:active|from|prepare|start)\b/.test(String(n?.className||''))}
function focus(opener,dialog){const a=document.activeElement,role=dialog?.closest('[role="dialog"]')||dialog?.querySelector('[role="dialog"]');return{tag:a?.tagName||null,isBody:a===document.body,isOpener:a===opener,insideTarget:!!role?.contains(a)}}
function observe(label,e){
 const dialogs=[...document.querySelectorAll('[role="dialog"]')].filter(visible).map(role=>{
  const ids=String(role.getAttribute('aria-labelledby')||'').split(/\s+/).filter(Boolean),resolved=compact(ids.map(id=>document.getElementById(id)?.textContent||'').join(' ')),title=compact(role.querySelector('.ant-modal-title')?.textContent),allowed=['新增令牌','编辑令牌']
  return{role:role.getAttribute('role'),title:allowed.includes(title)?title:'[UNEXPECTED_TITLE]',ariaModal:role.getAttribute('aria-modal'),labelledByCount:ids.length,resolvedName:allowed.includes(resolved)?resolved:'[UNEXPECTED_OR_MISSING_NAME]',activeInside:role.contains(document.activeElement)}
 })
 if(label==='settled'){
  const role=e.dialog.closest('[role="dialog"]')||e.dialog.querySelector('[role="dialog"]')
  cur.checks.push({label:e.title+' actual role declares modal=true',category:'primary',passed:role?.getAttribute('aria-modal')==='true'})
  cur.checks.push({label:e.title+' original native role/name stays correct',category:'existing-name',passed:dialogs.length===1&&dialogs[0].role==='dialog'&&dialogs[0].resolvedName===e.title})
  cur.checks.push({label:e.title+' original naturally-settled focus stays inside',category:'existing-focus',passed:!!role?.contains(document.activeElement)})
 }
 cur.observations.push({label,title:e.title,dialogs,entering:entering(e.dialog),focus:focus(e.opener,e.dialog),timing:label==='admitted'?'after native click plus ordinary helper settle; NOT immediate':'natural lifecycle readback',tokenSecretNeverProvided:true})
}
async function appPage(){
 user.setStoredCurrentUser(ACTOR)
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/access-tokens',component:Page}]});await router.push('/adapter/access-tokens');await router.isReady()
 const app=mount(App,{attachTo:document.body,global:{plugins:[Antd,router],stubs:{transition:false,'transition-group':false},config:{errorHandler:e=>runtime.push(e?.name||'vue-error')}}});roots.push({app,router});await settle()
 const w=app.findComponent(Page);assert.ok(w.exists(),'actual AccessTokenManagement through current App')
 await until(()=>!w.vm.tableLoading&&w.vm.dataSource.some(r=>r.id===72001),'actual raw-array DTO becomes native row')
 cur.checks.push({label:'actual App zhCN plus current lawful owner admission',passed:app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn'&&w.vm.canLoadForCurrentUser()})
 return w
}
async function open(w,mode){
 const title=mode==='add'?'新增令牌':'编辑令牌',opener=mode==='add'?button(w.element,'新增令牌'):button(w.element.querySelector('tr[data-row-key="72001"]'),'编辑');await click(opener)
 const dialog=await until(()=>[...document.querySelectorAll('.ant-modal')].find(n=>visible(n)&&compact(n.querySelector('.ant-modal-title')?.textContent)===title),'actual '+title),e={w,mode,title,opener,dialog};observe('admitted',e)
 await until(()=>!entering(dialog),'original real Transition naturally settles');await settle();observe('settled',e);return e
}
async function cancel(e){
 const cancel=button(e.dialog.querySelector('.ant-modal-footer'),'取消');await click(cancel);await until(()=>!e.w.vm.visible&&!visible(e.dialog),'native clean Cancel hides current shared Modal');await settle()
 cur.checks.push({label:e.title+' original Cancel returns exact own opener',category:'existing-focus',passed:document.activeElement===e.opener})
 cur.observations.push({label:'closed-settled',title:e.title,oldPanelConnected:e.dialog.isConnected,oldPanelVisible:visible(e.dialog),focus:focus(e.opener,e.dialog),visibleDialogCount:[...document.querySelectorAll('[role="dialog"]')].filter(visible).length})
}
function write(){if(process.env.TOKEN_MODAL_LAYER_LEDGER)fs.writeFileSync(process.env.TOKEN_MODAL_LAYER_LEDGER,JSON.stringify({schema:'ACTUAL_APP_TOKEN_MODAL_LAYER_CONTRACT_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),sourceStable:JSON.stringify(sourceBefore)===JSON.stringify(hashes()),cases,actualApp:true,legacyFallback:true,modalAttributeContractOnly:true,physicalTab:{qualified:false,reason:'jsdom focus/dispatch is not genuine keyboard Tab; Root Cua owns it'},tokenSecretNeverProvided:true,noClipboard:true},null,2)+'\n')}
test('TM01 actual App Add/Edit shared Token Modal active declaration preserves readonly flow',{timeout:30000},async()=>{
 cur={name:'TO01',completedFlow:false,passed:false,checks:[],observations:[]};cases.push(cur)
 try{
  const w=await appPage(),initial=calls.length,add=await open(w,'add');await cancel(add);const edit=await open(w,'edit');observe('shared-panel-context',edit);cur.checks.push({label:'same physical shared Modal panel is preserved',category:'existing-owner',passed:add.dialog===edit.dialog});cur.observations.push({label:'shared native panel identity',sameNode:add.dialog===edit.dialog});await cancel(edit)
  cur.checks.push({label:'modal actions add no API reads and no nonGET writes',passed:calls.length===initial&&calls.every(c=>c.method==='get')})
  cur.checks.push({label:'zero clipboard/real IO/runtime/console errors',passed:realIO.length===0&&runtime.length===0&&errors.length===0})
  cur.completedFlow=true;assert.deepEqual(cur.checks.filter(c=>!c.passed).map(c=>c.label),[],'all qualified modal and native preservation checks');cur.passed=true
 }finally{write()}
})
afterEach(async()=>{try{for(const r of roots.splice(0)){r.app.unmount();r.router.options.history.destroy()}await settle();Antd.Modal.destroyAll();Antd.message.destroy();user.clearStoredCurrentUser();localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle();cur.io={calls:[...calls],writes:calls.filter(c=>c.method!=='get').length,realIO:[...realIO],runtimeCount:runtime.length,consoleErrorCount:errors.length,consoleWarningCount:warnings.length};cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(errors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}finally{write()}})
after(()=>{Swal.fire=oldSwalFire;axios.defaults.adapter=originalAdapter;console.error=oldError;console.warn=oldWarn;write()})
