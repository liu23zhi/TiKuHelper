/* Real current App + UserList + Antd/zhCN/fallback, NOT old custom Shell.
 * Active-layer contract only; no DELETE/business write or focus-manager rewrite. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}};globalThis.getComputedStyle=window.getComputedStyle
for(const k of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','HTMLTextAreaElement','CustomEvent'])globalThis[k]=window[k]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick}=require('vue'),{createRouter,createMemoryHistory}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const user=require(path.join(web,'src/utils/currentUser.js'))
const ACTOR={id:65001,username:'SYNTHETIC_USERLIST_ADMIN',role:'admin',status:'enabled'}
const ROW={id:71,username:'SYNTHETIC_MEMBER',nickname:'合成用户',role:'user',status:'enabled',balance:1.234567,phone:'',wechatId:'',perms:'search',registerSource:'admin',lastLoginIp:'',registerIp:''}
const cases=[],roots=[],calls=[],realIO=[],runtime=[],errors=[],warnings=[];let cur
const compact=v=>String(v??'').replace(/\s+/g,'').trim(),H=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')
const inputs=['src/App.vue','src/pages/UserList/index.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/userPortalHelpers.mjs','src/utils/legacyFormAccessibility.mjs','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputs.map(p=>[p,H(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=H(__filename)
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_USER_MODAL_IO_'+kind)}
const originalAdapter=axios.defaults.adapter
assert.equal(!!require.cache[path.join(web,'src/utils/request.js')],false,'inert adapter before actual request/API import')
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url);calls.push({method,url,paramsKeys:Object.keys(config.params||{}).sort()})
 if(method!=='get')return Promise.reject(deny('nonGET-denied'))
 let data
 if(url==='/adapter-service/public/register-status')data={siteName:'Synthetic UserList App'}
 else if(url==='/adapter-service/system-settings')data={newUserBonusAmount:.123456}
 else if(url==='/adapter-service/user')data={data:[{...ROW}],total:1,page:Number(config.params?.page)||1,pageSize:Number(config.params?.pageSize)||10}
 else return Promise.reject(deny('unknownGET-denied'))
 return Promise.resolve({status:200,data,headers:{'x-request-id':calls.length.toString(16).padStart(32,'0')},config,request:{memoryOnly:true}})
}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(e.error?.name||'window-error'));window.addEventListener('unhandledrejection',e=>runtime.push(e.reason?.name||'unhandled-rejection'))
const oldError=console.error,oldWarn=console.warn
console.error=(...a)=>{errors.push({argumentCount:a.length});return Reflect.apply(oldError,console,a)};console.warn=(...a)=>{warnings.push({argumentCount:a.length});return Reflect.apply(oldWarn,console,a)}
const App=require(path.join(web,'src/App.vue')),Page=require(path.join(web,'src/pages/UserList/index.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(fn,label){const end=Date.now()+7000;while(Date.now()<end){const v=fn();if(v)return v;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=getComputedStyle(p);if(p.hidden||p.inert||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
function button(scope,text){assert.ok(scope,'owned control scope');const found=[...scope.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent)===compact(text));assert.equal(found.length,1,'one native '+text);return found[0]}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled);n.focus();n.click();await settle()}
function entering(n){return /-(?:enter|appear)-(?:active|from|prepare|start)\b/.test(String(n?.className||''))}
const TITLES=['新增用户','编辑用户','重置用户密码','修改用户余额']
function modalByTitle(title){const a=[...document.querySelectorAll('.ant-modal')].filter(n=>visible(n)&&compact(n.querySelector('.ant-modal-title')?.textContent)===title);assert.ok(a.length<=1,'no duplicate owned title');return a[0]||null}
function safeTitle(n){const text=compact(n?.textContent);return TITLES.includes(text)?text:'[UNEXPECTED_OR_MISSING_TITLE]'}
function focusState(opener,target){
 const active=document.activeElement,role=target?.closest('[role="dialog"]')||target?.querySelector('[role="dialog"]')
 return{tag:active?.tagName||null,isBody:active===document.body,isOpener:active===opener,insideTarget:!!role?.contains(active)}
}
function snapshot(label,e,parent){
 const dialogs=[...document.querySelectorAll('[role="dialog"]')].filter(visible).map(role=>{
  const ids=String(role.getAttribute('aria-labelledby')||'').split(/\s+/).filter(Boolean),name=compact(ids.map(id=>document.getElementById(id)?.textContent||'').join(' '))
  const title=role.querySelector('.ant-modal-title')
  return{role:role.getAttribute('role'),title:safeTitle(title),ariaModal:role.getAttribute('aria-modal'),labelledByCount:ids.length,resolvedName:TITLES.includes(name)?name:'[UNEXPECTED_OR_MISSING_NAME]',activeInside:role.contains(document.activeElement)}
 })
 if(label==='settled'||label==='parent-after-child-cancel'){
  const ownRole=e.dialog.closest('[role="dialog"]')||e.dialog.querySelector('[role="dialog"]')
  cur.checks.push({label:label+' '+e.title+' active native role is modal=true',category:'primary',passed:ownRole?.getAttribute('aria-modal')==='true'})
  cur.checks.push({label:label+' existing native names resolve',category:'existing-name',passed:dialogs.every(d=>d.role==='dialog'&&d.resolvedName===d.title)})
  if(parent){
   const parentRole=parent.dialog.closest('[role="dialog"]')||parent.dialog.querySelector('[role="dialog"]')
   cur.checks.push({label:'nested parent remains open but not current modal',category:'layer-guard',passed:e.w.vm.visible&&visible(parent.dialog)&&parentRole?.getAttribute('aria-modal')!=='true'&&dialogs.length===2})
  }
  if(label==='parent-after-child-cancel')cur.checks.push({label:'child Cancel restores original parent active layer',category:'layer-guard',passed:e.w.vm.visible&&!e.w.vm.balanceVisible&&visible(e.dialog)&&dialogs.length===1})
 }
 cur.observations.push({label,title:e.title,entering:entering(e.dialog),dialogs,focus:focusState(e.opener,e.dialog),parentEditorOpen:parent?e.w.vm.visible:null,parentPanelVisible:parent?visible(parent.dialog):null,passwordInputCount:e.dialog.querySelectorAll('input[type="password"]').length,noPasswordEntered:true})
}
async function appPage(){
 user.setStoredCurrentUser(ACTOR)
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/userlist',component:Page}]});await router.push('/adapter/userlist');await router.isReady()
 const app=mount(App,{attachTo:document.body,global:{plugins:[Antd,router],stubs:{transition:false,'transition-group':false},config:{errorHandler:e=>runtime.push(e?.name||'vue-error')}}});roots.push({app,router});await settle()
 const w=app.findComponent(Page);assert.ok(w.exists(),'actual UserList through current App RouterView')
 await until(()=>!w.vm.tabLoading&&w.vm.dataSource.some(r=>r.id===71)&&calls.some(c=>c.url==='/adapter-service/system-settings'),'actual legal admin read/DTO')
 cur.checks.push({label:'real App zhCN and UserList admin guard',passed:app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn'&&w.vm.canManageUsers})
 return w
}
async function open(w,kind,parent){
 let opener,title
 if(kind==='add'){opener=button(w.element.querySelector('.section-actions'),'新增用户');title='新增用户'}
 else if(kind==='balance'){opener=button(parent.dialog,'修改余额');title='修改用户余额'}
 else{const row=w.element.querySelector('.desktop-user-table tr[data-row-key="71"]');opener=button(row,kind==='edit'?'编辑':'重置密码');title=kind==='edit'?'编辑用户':'重置用户密码'}
 await click(opener);const dialog=await until(()=>modalByTitle(title),'actual '+title),e={w,kind,title,opener,dialog};snapshot('admitted',e,parent)
 await until(()=>!entering(dialog),'natural original Modal enter');await settle();snapshot('settled',e,parent);return e
}
async function cancel(e,parent){
 const passwords=[...e.dialog.querySelectorAll('input[type="password"]')],cancel=button(e.dialog.querySelector('.ant-modal-footer'),'取消');cancel.focus();cancel.click()
 cur.observations.push({label:'cancel-synchronous',title:e.title,targetPanelConnected:e.dialog.isConnected,targetPanelVisible:visible(e.dialog),passwordNodesWereEmpty:passwords.every(n=>n.value===''),noPopulatedPasswordErasureClaim:true,parentEditorOpen:parent?e.w.vm.visible:null})
 await settle();await until(()=>!visible(e.dialog),'normal native Cancel hides target Modal');await settle()
 cur.checks.push({label:e.title+' original Cancel returns exact own opener',category:'existing-focus',passed:document.activeElement===e.opener})
 if(parent)cur.checks.push({label:'child Cancel preserves open parent',category:'layer-guard',passed:e.w.vm.visible&&visible(parent.dialog)})
 cur.observations.push({label:'closed-settled',title:e.title,targetPanelConnected:e.dialog.isConnected,targetPanelVisible:visible(e.dialog),passwordNodesStillEmpty:passwords.every(n=>n.value===''),visibleDialogTitles:[...document.querySelectorAll('[role="dialog"]')].filter(visible).map(n=>safeTitle(n.querySelector('.ant-modal-title'))),focus:focusState(e.opener,e.dialog),parentEditorOpen:parent?e.w.vm.visible:null,parentPanelVisible:parent?visible(parent.dialog):null})
}
function write(){
 const after=hashes();if(process.env.USER_MODAL_LAYER_LEDGER)fs.writeFileSync(process.env.USER_MODAL_LAYER_LEDGER,JSON.stringify({schema:'ACTUAL_APP_USER_MODAL_LAYER_CONTRACT_V1',fixtureSHA256,sourceBefore,sourceAfter:after,sourceStable:JSON.stringify(sourceBefore)===JSON.stringify(after),cases,actualApp:true,legacyFallback:true,physicalBrowserOrAT:false,currentLayerContractOnly:true,noPopulatedPasswordErasureClaim:true},null,2)+'\n')
}
function register(name,fn){test(name,{timeout:30000},async()=>{cur={name,checks:[],observations:[],completedFlow:false,passed:false};cases.push(cur);try{const w=await appPage(),initialReads=calls.length;await fn(w);cur.completedFlow=true;cur.checks.push({label:'modal lifecycle issues no added API read',category:'existing-readonly',passed:calls.length===initialReads});cur.checks.push({label:'only approved synthetic GET; no writes/realIO/runtime',passed:calls.every(c=>c.method==='get')&&realIO.length===0&&runtime.length===0&&errors.length===0});assert.deepEqual(cur.checks.filter(c=>!c.passed).map(c=>c.label),[],'all qualified active-layer/native preservation checks');cur.passed=true}finally{write()}})}
register('UL01 actual App Add native active-modal declaration',async w=>{const e=await open(w,'add');await cancel(e)})
register('UL02 actual App Edit and nested Balance active layer handoff',async w=>{const parent=await open(w,'edit'),child=await open(w,'balance',parent);await cancel(child,parent);snapshot('parent-after-child-cancel',parent);await cancel(parent)})
register('UL03 actual App password native active-modal declaration without password input',async w=>{const e=await open(w,'password');await cancel(e)})
afterEach(async()=>{try{for(const r of roots.splice(0)){r.app.unmount();r.router.options.history.destroy()}await settle();Antd.Modal.destroyAll();Antd.message.destroy();user.clearStoredCurrentUser();localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle();cur.io={calls:[...calls],writes:calls.filter(c=>c.method!=='get').length,realIO:[...realIO],runtimeCount:runtime.length,consoleErrorCount:errors.length,consoleWarningCount:warnings.length};cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(errors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}finally{write();calls.length=realIO.length=runtime.length=errors.length=warnings.length=0}})
after(()=>{axios.defaults.adapter=originalAdapter;console.error=oldError;console.warn=oldWarn;write()})
