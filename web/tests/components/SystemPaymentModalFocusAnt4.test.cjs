/* Actual App (including delivered legacy fallback) + SystemSettings/Antd payment Modal.
 * Read-only transport; no create/update/delete/submit or raw SVG/record dump. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
assert.ok(path.isAbsolute(web),'absolute public source web')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
const helper=process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs');require(helper);require.extensions['.css']=m=>{m.exports={}};globalThis.getComputedStyle=window.getComputedStyle
for(const key of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','HTMLTextAreaElement','CustomEvent'])globalThis[key]=window[key]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,h}=require('vue'),{createRouter,createMemoryHistory}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const user=require(path.join(web,'src/utils/currentUser.js'))
const ACTOR={id:66201,username:'SYNTHETIC_LABEL_ADMIN',role:'admin',status:'enabled'}
const SVG='<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" fill="#0066cc"/></svg>',METHOD={id:71,name:'SYNTHETIC_PAYMENT_METHOD',iconSvg:SVG,epayType:'alipay',enabled:true,sort:3,remark:'SYNTHETIC_PAYMENT_REMARK'}
const cases=[],roots=[],calls=[],realIO=[],runtime=[],errors=[],warnings=[];let cur=null
const compact=x=>String(x??'').replace(/\s+/g,' ').trim(),H=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')
const inputs=['src/App.vue','src/pages/SystemSettings/index.vue','src/pages/SystemSettings/PaymentMethodPanel.vue','src/pages/SystemSettings/SystemMetricsPanel.vue','src/pages/SystemSettings/components/PaymentMethodIconPicker.vue','src/utils/legacyFormAccessibility.mjs','src/utils/currentUser.js','src/utils/request.js','src/api/TikuComponentApi.js','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputs.map(p=>[p,H(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=H(__filename)
function check(value,label,category='independent',qualified=true){cur.checks.push({label,category,qualified,passed:!!value})}
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_PAYMENT_LABEL_IO')}
const oldAdapter=axios.defaults.adapter
assert.equal(!!require.cache[path.join(web,'src/utils/request.js')],false,'actual adapter precedes API/request load')
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url);calls.push({method,url})
 const response=data=>Promise.resolve({status:200,data,headers:{},config,request:{memoryOnly:true}})
 if(method!=='get')return Promise.reject(deny('mutation-refused'))
 if(url==='/adapter-service/public/register-status')return response({siteName:'Synthetic label app'})
 if(url==='/adapter-service/system-settings')return response({siteName:'Synthetic settings',maxLogSizeBytes:2*1024*1024*1024,minRechargeAmount:1,captchaProvider:'local'})
 if(url==='/adapter-service/payment-methods')return response([{...METHOD}])
 if(url==='/adapter-service/system-metrics')return response({schemaVersion:1,startedAt:'2026-11-01T00:00:00Z',uptimeSeconds:10,http:{total:0,inFlight:0,failed:0,latencyCount:0,latencyTotalMillis:0,latencyMaxMillis:0,buckets:[]},cache:{search:{hits:0,misses:0,shared:0},fallback:{hits:0,misses:0,shared:0}},billing:{attempts:0,successes:0,failures:0}})
 return Promise.reject(deny('unexpected-get'))
}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(e.error?.name||'window-error'));window.addEventListener('unhandledrejection',e=>runtime.push(e.reason?.name||'unhandled-rejection'))
const oldError=console.error,oldWarn=console.warn
console.error=(...args)=>{errors.push({argumentCount:args.length});return Reflect.apply(oldError,console,args)};console.warn=(...args)=>{warnings.push({argumentCount:args.length});return Reflect.apply(oldWarn,console,args)}
const App=require(path.join(web,'src/App.vue')),Page=require(path.join(web,'src/pages/SystemSettings/index.vue')),PaymentPanel=require(path.join(web,'src/pages/SystemSettings/PaymentMethodPanel.vue')),MetricsPanel=require(path.join(web,'src/pages/SystemSettings/SystemMetricsPanel.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(fn,label){const end=Date.now()+9000;while(Date.now()<end){const value=fn();if(value)return value;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=window.getComputedStyle(p);if(p.hidden||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
function button(root,label){const list=[...root.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent).replace(/\s+/g,'')===compact(label).replace(/\s+/g,''));assert.equal(list.length,1,'one native '+label);return list[0]}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled,'actual enabled native control');n.focus?.();n.click();await settle()}
const focusSpies=new Map()
function spyFocus(node){
 let state=focusSpies.get(node)
 if(!state){const descriptor=Object.getOwnPropertyDescriptor(node,'focus'),native=node.focus;state={descriptor,native,count:0};focusSpies.set(node,state);node.focus=function(...args){state.count++;return Reflect.apply(native,this,args)}}
 const before=state.count;return()=>state.count-before
}
function entering(n){return /-(?:enter|appear)-(?:active|from|prepare|start)\b/.test(String(n?.className||''))}
async function readyParent(w){await until(()=>w.vm.settingsLoaded&&w.vm.paymentMethodsLoaded&&!w.vm.tabLoading&&!w.vm.settingsDraining,'current parent ready; no actor metrics requirement')}
async function appPage(){
 user.setStoredCurrentUser(ACTOR)
 const Host={render:()=>h('section',[h(Page)])},router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/system-settings',component:Host}]})
 await router.push('/adapter/system-settings');await router.isReady()
 const app=mount(App,{attachTo:document.body,global:{plugins:[Antd,router],stubs:{transition:false,'transition-group':false},config:{errorHandler:e=>runtime.push(e?.name||'vue-error')}}})
 roots.push({app,router});await settle();const pages=app.findAllComponents(Page);assert.equal(pages.length,1,'one real Page inside App');const w=pages[0];await readyParent(w)
 const metrics=w.findComponent(MetricsPanel);assert.ok(metrics.exists());await until(()=>metrics.vm.metrics&&!metrics.vm.loading,'initial actual typed metrics only')
 check(w.vm.canManageSettings(),'real cached admin admission','setup');check(app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn','real App zhCN','setup')
 return{w,app}
}
function panel(w){const p=w.findComponent(PaymentPanel);assert.ok(p.exists());return p}
async function openPayment(w,mode='edit'){
 const p=panel(w),opener=mode==='add'?button(p.element,'新增支付方式'):button(p.element.querySelector('tr[data-row-key="71"]'),'编辑')
 await click(opener)
 const title=mode==='add'?'新增支付方式':'编辑支付方式',dialog=await until(()=>[...document.querySelectorAll('.ant-modal')].find(n=>visible(n)&&compact(n.querySelector('.ant-modal-title')?.textContent)===title),'actual '+title)
 await until(()=>!entering(dialog),'natural Transition settles');await settle()
 return{w,opener,dialog,mode,title,role:dialog.closest('[role="dialog"]')||dialog.querySelector('[role="dialog"]')}
}
function modalSemantics(e){
 const ids=String(e.role?.getAttribute('aria-labelledby')||'').split(/\s+/).filter(Boolean)
 check(e.role?.getAttribute('role')==='dialog','existing actual role ancestor','existing-name')
 check(ids.some(id=>compact(document.getElementById(id)?.textContent)===e.title),'existing visible modal title resolves','existing-name')
 check(e.role?.getAttribute('aria-modal')==='true','payment '+e.mode+' actual role is modal=true','goal-modal')
}
function cancelNow(e,label){
 const form=e.w.vm.paymentMethodForm?.$el||e.w.vm.paymentMethodForm,controls=[...form.querySelectorAll('input,textarea')],hadNonempty=controls.some(n=>String(n.value||'').length>0),count=spyFocus(e.opener)
 const cancel=button(e.dialog.querySelector('.ant-modal-footer'),'取消');cancel.focus();assert.strictEqual(document.activeElement,cancel);cancel.click()
 const hidden=!!(e.role?.hidden&&e.role.style.display==='none'),wiped=controls.length>0&&controls.every(n=>n.value==='')
 check(hidden,label+' private role hidden synchronously','privacy')
 check(wiped,label+' native values wiped synchronously without dump','privacy')
 check(e.w.vm.paymentMethodMounted&&!e.w.vm.paymentMethodVisible,label+' original two-tick subtree drain retained','privacy')
 if(e.mode==='edit')check(hadNonempty,label+' edit contained synthetic data before wipe','privacy')
 cur.observations.push({label,hiddenImmediately:hidden,allValuesWiped:wiped,nativeInputCount:controls.length,hadNonempty,stillMountedBeforeTicks:e.w.vm.paymentMethodMounted})
 return{...e,count}
}
async function drained(e){await until(()=>!e.w.vm.paymentMethodMounted&&!e.dialog.isConnected,'original private dialog drain');await settle()}
function foreground(w){const form=w.vm.settingsForm?.$el||w.element.querySelector('form'),n=form?.querySelector('input:not([type="password"]):not([type="hidden"])');assert.ok(n&&visible(n)&&!n.disabled,'legitimate main settings foreground input');return n}
async function cleanReturn(e,label){
 const q=cancelNow(e,label);await drained(q)
 check(document.activeElement===e.opener,label+' clean Cancel restores exact owned opener after drain','goal-return')
 check(q.count()===1,label+' only one new return-focus call','goal-return-dependent',document.activeElement===e.opener)
 cur.observations.push({label:label+' drained',openerConnected:e.opener.isConnected,activeBody:document.activeElement===document.body,returnFocusCalls:q.count()})
}
function writeLedger(){
 const after=hashes(),out={schema:'SYSTEM_PAYMENT_MODAL_FOCUS_CONTRACT_V1',fixtureSHA256,sourceBefore,sourceAfter:after,sourceStable:JSON.stringify(sourceBefore)===JSON.stringify(after),cases,actualAppLegacyFallback:true,actualTransitions:true,physicalBrowserOrAT:false}
 if(process.env.SYSTEM_PAYMENT_FOCUS_LEDGER)fs.writeFileSync(process.env.SYSTEM_PAYMENT_FOCUS_LEDGER,JSON.stringify(out,null,2)+'\n')
}
function register(name,fn){test(name,{timeout:40000},async()=>{
 cur={name,checks:[],observations:[],completedFlow:false,passed:false};cases.push(cur)
 try{
  await fn();cur.completedFlow=true
  check(calls.every(c=>c.method==='get'),'only allowlisted GETs; zero business writes','safety')
  check(realIO.length===0&&runtime.length===0&&errors.length===0,'zero realIO/runtime/console errors','safety')
  assert.deepEqual(cur.checks.filter(c=>c.qualified&&!c.passed).map(c=>c.label),[],'all qualified focus/privacy observations')
  cur.passed=true
 }finally{writeLedger()}
})}
register('PM01 actual App clean Add/Edit modal semantics and after-private-drain opener return',async()=>{
 const {w}=await appPage();const reads=calls.length
 const add=await openPayment(w,'add');modalSemantics(add);await cleanReturn(add,'Add')
 const edit=await openPayment(w,'edit');modalSemantics(edit);await cleanReturn(edit,'Edit')
 check(calls.length===reads,'clean modal lifecycle adds no API read or write','existing-readonly')
})
register('PM02 clean-close focus ticket preserves foreground, other real dialog and newer payment owner',async()=>{
 const {w}=await appPage()
 let e=await openPayment(w),q=cancelNow(e,'foreground'),input=foreground(w);input.focus();assert.strictEqual(document.activeElement,input);await drained(q)
 check(document.activeElement===input&&q.count()===0,'legitimate foreground is never replaced','guard')
 e=await openPayment(w);q=cancelNow(e,'other-dialog')
 const other=mount(Antd.Modal,{attachTo:document.body,props:{open:true,title:'SYNTHETIC_OTHER_MODAL',focusTriggerAfterClose:false},global:{plugins:[Antd],stubs:{transition:false,'transition-group':false}}});roots.push({app:other,router:null})
 await until(()=>[...document.querySelectorAll('[role="dialog"]')].some(n=>n!==e.role&&visible(n)),'other actual Antd modal admitted');await drained(q)
 check(q.count()===0,'other visible modal prevents old opener return','guard')
 other.unmount();await settle()
 e=await openPayment(w);q=cancelNow(e,'new-owner')
 const create=button(panel(w).element,'新增支付方式');assert.ok(!create.disabled);create.click()
 await until(()=>w.vm.paymentMethodMounted&&w.vm.paymentMethodVisible&&w.vm.paymentMethodModalTitle==='新增支付方式','new original payment owner admitted');await settle()
 check(q.count()===0,'newer original dialog owns focus; old return ticket retired','guard')
 const freshDialog=[...document.querySelectorAll('.ant-modal')].find(n=>visible(n)&&compact(n.querySelector('.ant-modal-title')?.textContent)==='新增支付方式');assert.ok(freshDialog)
 const fresh={w,opener:create,dialog:freshDialog,mode:'add',title:'新增支付方式',role:freshDialog.closest('[role="dialog"]')};await drained(cancelNow(fresh,'new-owner-cleanup'))
})
register('PM03 actor, panel-read refresh and unmount never revive pending old close focus',async()=>{
 const {w,app}=await appPage()
 let e=await openPayment(w),q=cancelNow(e,'actor');user.setStoredCurrentUser({...ACTOR,id:66202,username:'SYNTHETIC_OTHER_ADMIN'});await drained(q);await readyParent(w)
 check(q.count()===0,'actor retirement prevents old return','guard')
 // Metrics intentionally need not reload after actor retirement.
 e=await openPayment(w);q=cancelNow(e,'panel-read');const before=calls.length,refresh=button(panel(w).element,'刷新');assert.ok(!refresh.disabled);refresh.click();await drained(q);await until(()=>calls.length>before&&!w.vm.paymentMethodLoading,'explicit panel read finished')
 check(q.count()===0,'new payment read/data scope prevents old return','guard')
 e=await openPayment(w);q=cancelNow(e,'unmount');app.unmount();await settle();await settle()
 check(q.count()===0&&!e.opener.isConnected,'unmounted frame never invokes retired opener focus','guard')
})
afterEach(async()=>{
 try{
  for(const r of roots.splice(0)){if(r.app.exists())r.app.unmount();r.router?.options.history.destroy()}
  await settle();Antd.Modal.destroyAll();Antd.message.destroy();user.clearStoredCurrentUser();localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle()
  for(const[node,s]of focusSpies){if(s.descriptor)Object.defineProperty(node,'focus',s.descriptor);else delete node.focus}focusSpies.clear()
  cur.io={calls:[...calls],zeroMutation:calls.every(c=>c.method==='get'),realIO:[...realIO],runtimeCount:runtime.length,consoleErrorCount:errors.length,consoleWarningCount:warnings.length}
  cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)}
  assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(errors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))
 }finally{writeLedger();calls.length=realIO.length=runtime.length=errors.length=warnings.length=0}
})
after(()=>{axios.defaults.adapter=oldAdapter;console.error=oldError;console.warn=oldWarn;writeLedger()})
