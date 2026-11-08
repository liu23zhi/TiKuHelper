/* Actual App/Antd/TicketsPage ordinary readonly list/detail with real request and strict inert transport.
 * Observation only. Synthetic own ticket only; no create/reply/status/notifications/clipboard/real IO or physical Tab. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}};globalThis.getComputedStyle=window.getComputedStyle
for(const k of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','HTMLTextAreaElement','CustomEvent'])globalThis[k]=window[k]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,h}=require('vue'),{createRouter,createMemoryHistory}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const user=require(path.join(web,'src/utils/currentUser.js'))
const ACTOR={id:1906201,role:'user',username:'synthetic_ticket_viewer',status:'enabled',balance:3.234567,minRechargeAmount:1}
const ROW={id:8094,userId:1906201,userName:'synthetic_ticket_viewer',subject:'SYNTHETIC_READONLY_TICKET',status:'open',createdAt:'2026-11-02T00:00:00Z',updatedAt:'2026-11-02T00:00:00Z'},THREAD={id:1,body:'SYNTHETIC_READONLY_THREAD',authorRole:'user',authorName:'Synthetic',createdAt:'2026-11-02T00:00:00Z'}
const cases=[],roots=[],calls=[],realIO=[],runtime=[],errors=[],warnings=[];let cur;let activePlan={start:0,actorB:false}
const compact=x=>String(x??'').replace(/\s+/g,'').trim(),H=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')
const inputs=['src/App.vue','src/pages/Tickets/index.vue','src/api/TicketApi.js','src/utils/ticketHelpers.mjs','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/legacyFormAccessibility.mjs','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputs.map(p=>[p,H(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=H(__filename)
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_SHARED_BILLING_MODAL_IO_'+kind)}
const originalAdapter=axios.defaults.adapter
assert.equal(!!require.cache[path.join(web,'src/utils/request.js')],false,'inert adapter before actual request/API import')
const exactParams=(actual,expected)=>Object.keys(actual).length===Object.keys(expected).length&&Object.keys(expected).every(k=>Object.prototype.hasOwnProperty.call(actual,k)&&Object.is(actual[k],expected[k]))
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url),params=config.params||{},paramsKeys=Object.keys(params).sort();calls.push({method,url,paramsKeys,...(url==='/adapter-service/tickets'?{params:{page:params.page,pageSize:params.pageSize}}:{})})
 if(method!=='get')return Promise.reject(deny('nonGET'))
 if(calls.slice(activePlan.start).filter(c=>c.url===url).length>(url==='/adapter-service/tickets'?2:(url==='/adapter-service/tickets/8094'&&activePlan.actorB?2:1)))return Promise.reject(deny('unplanned-repeated-GET'))
 let data,status=200;const actorB=activePlan.actorB&&Number(user.getStoredCurrentUser()?.id)===1906202
 if(url==='/adapter-service/public/register-status'){if(!exactParams(params,{}))return Promise.reject(deny('unexpected-public-params'));data={siteName:'Synthetic Tickets Readonly App'}}
 else if(url==='/adapter-service/tickets'){if(!exactParams(params,{page:1,pageSize:20}))return Promise.reject(deny('unexpected-list-params'));data={items:actorB?[]:[{...ROW}],total:actorB?0:1,page:1,pageSize:20}}
 else if(url==='/adapter-service/tickets/8094'){if(!exactParams(params,{}))return Promise.reject(deny('unexpected-detail-params'));if(actorB){status=404;data={message:'Synthetic owned record unavailable'}}else data={ticket:{...ROW},messages:[{...THREAD}],notification:null}}
 else return Promise.reject(deny('unknownGET'))
 return Promise.resolve({status,data,headers:{},config,request:{memoryOnly:true}})
}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
for(const n of new Set([window.navigator,globalThis.navigator].filter(Boolean)))Object.defineProperty(n,'clipboard',{configurable:true,value:{writeText:()=>deny('clipboard-write'),readText:()=>deny('clipboard-read')}})
document.execCommand=()=>deny('clipboard-command')
window.addEventListener('error',e=>runtime.push(e.error?.name||'window-error'));window.addEventListener('unhandledrejection',e=>runtime.push(e.reason?.name||'unhandled-rejection'))
const oldError=console.error,oldWarn=console.warn
console.error=(...a)=>{errors.push({argumentCount:a.length});return Reflect.apply(oldError,console,a)};console.warn=(...a)=>{warnings.push({argumentCount:a.length});return Reflect.apply(oldWarn,console,a)}
const Swal=require('sweetalert2'),oldSwalFire=Swal.fire;Swal.fire=()=>deny('swal-popup')
const App=require(path.join(web,'src/App.vue')),Page=require(path.join(web,'src/pages/Tickets/index.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(fn,label){const end=Date.now()+7000;while(Date.now()<end){const value=fn();if(value)return value;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=getComputedStyle(p);if(p.hidden||p.inert||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
function button(scope,title){assert.ok(title==='查看详情','only admits original readonly detail row control');assert.ok(scope);const a=[...scope.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent)===title);assert.equal(a.length,1,'one original native '+title);return a[0]}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled);n.focus();n.click();await settle()}
function entering(n){return /-(?:enter|appear)-(?:active|from|prepare|start)\b/.test(String(n?.className||''))}
function focus(opener,dialog){const a=document.activeElement,role=dialog?.closest('[role="dialog"]')||dialog?.querySelector('[role="dialog"]');return{tag:a?.tagName||null,isBody:a===document.body,isOpener:a===opener,insideTarget:!!role?.contains(a)}}
function observe(label,e){
 const role=e.dialog.closest('[role="dialog"]')||e.dialog.querySelector('[role="dialog"]'),ids=String(role?.getAttribute('aria-labelledby')||'').split(/\s+/).filter(Boolean),name=compact(ids.map(id=>document.getElementById(id)?.textContent||'').join(' ')),reply=e.dialog.querySelector('.reply-form'),send=reply&&[...reply.querySelectorAll('button')].find(n=>compact(n.textContent)==='发送回复')
 cur.observations.push({label,title:compact(e.dialog.querySelector('.ant-modal-title')?.textContent)===ROW.subject?'SYNTHETIC_READONLY_TICKET':'[OTHER_OR_LOADING_TITLE]',role:role?.getAttribute('role')||null,ariaModal:role?.getAttribute('aria-modal')||null,labelledByCount:ids.length,resolvedName:name===ROW.subject?'SYNTHETIC_READONLY_TICKET':'[OTHER_OR_LOADING_NAME]',entering:entering(e.dialog),focus:focus(e.opener,e.dialog),messageCount:e.dialog.querySelectorAll('.message-item').length,syntheticThreadVisible:[...e.dialog.querySelectorAll('.message-item p')].some(n=>n.textContent===THREAD.body),ordinaryAdminStatusControls:e.dialog.querySelectorAll('.detail-actions').length,replySendDisabled:send?.disabled===true,route:e.router.currentRoute.value.path,timing:label==='settled'?'natural Transition settled; not immediate focus':'after original native view click and response ready'})
}
async function appPage(){
 user.setStoredCurrentUser(ACTOR)
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/tickets/:id?',component:Page},{path:'/fixture-other',component:{render:()=>h('div',{class:'fixture-other-route'},'SYNTHETIC_OTHER_ROUTE')}}]});await router.push('/adapter/tickets');await router.isReady()
 const app=mount(App,{attachTo:document.body,global:{plugins:[Antd,router],stubs:{transition:false,'transition-group':false},config:{errorHandler:e=>runtime.push(e?.name||'vue-error')}}});roots.push({app,router});await settle()
 const w=app.findComponent(Page);assert.ok(w.exists(),'actual TicketsPage through current App')
 await until(()=>w.element.querySelector('tr[data-row-key="8094"]')?.textContent.includes(ROW.subject),'actual ordinary ticket list DTO becomes original native row')
 cur.checks.push({label:'actual App zhCN and lawful ordinary list without admin configuration',passed:app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn'&&!w.element.querySelector('.settings-panel')})
 return {w,router}
}
function write(){if(process.env.TICKETS_READONLY_LEDGER)fs.writeFileSync(process.env.TICKETS_READONLY_LEDGER,JSON.stringify({schema:'ACTUAL_APP_TICKETS_CLOSE_FOCUS_CONTRACT_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),sourceStable:JSON.stringify(sourceBefore)===JSON.stringify(hashes()),cases,actualApp:true,legacyFallback:true,observationNotSemanticAcceptance:false,physicalTab:{qualified:false,reason:'jsdom focus/dispatch is not genuine keyboard Tab; Root Cua independent'},physicalEscape:{qualified:false,reason:'no genuine Escape action'},syntheticOnly:true,noClipboard:true},null,2)+'\n')}
test('TK01 actual App ordinary Ticket detail declares native modal and preserves readonly route close',{timeout:30000},async()=>{
 cur={name:'TK01',completedFlow:false,passed:false,checks:[],observations:[]};cases.push(cur)
 try{
  const {w,router}=await appPage(),opener=button(w.element.querySelector('tr[data-row-key="8094"]'),'查看详情');await click(opener)
  await until(()=>router.currentRoute.value.path==='/adapter/tickets/8094'&&calls.some(c=>c.url==='/adapter-service/tickets/8094'),'original view action admits original route and real detail read')
  const dialog=await until(()=>[...document.querySelectorAll('.ant-modal')].find(n=>visible(n)&&compact(n.querySelector('.ant-modal-title')?.textContent)===ROW.subject&&n.querySelector('.message-item p')?.textContent===THREAD.body),'actual detail body from original GET response'),e={w,router,opener,dialog};observe('admitted',e)
  await until(()=>!entering(dialog),'real original Transition naturally settles');await settle();observe('settled',e)
  const role=dialog.closest('[role="dialog"]')||dialog.querySelector('[role="dialog"]'),ids=String(role?.getAttribute('aria-labelledby')||'').split(/\s+/).filter(Boolean),name=compact(ids.map(id=>document.getElementById(id)?.textContent||'').join(' '))
  cur.checks.push({label:'current native ticket detail role is dialog and name resolves to real title',category:'positive',qualified:true,passed:!!role&&role.getAttribute('role')==='dialog'&&name===ROW.subject})
  cur.checks.push({label:'current active native ticket detail declares aria-modal true',category:'primary',qualified:!!role,passed:role?.getAttribute('aria-modal')==='true'})
  cur.checks.push({label:'original naturally settled detail focus is inside owned native role',category:'positive',qualified:true,passed:!!role?.contains(document.activeElement)})
  const close=dialog.querySelector('button.ant-modal-close');assert.ok(close&&visible(close),'original native detail header X');await click(close)
  await until(()=>router.currentRoute.value.path==='/adapter/tickets'&&!visible(dialog),'original header close returns route and hides current detail');await settle()
  cur.observations.push({label:'closed-settled',route:router.currentRoute.value.path,oldDialogConnected:dialog.isConnected,oldDialogVisible:visible(dialog),openerConnected:opener.isConnected,openerEnabled:!opener.disabled,focus:focus(opener,dialog),connectedThreadCount:document.querySelectorAll('.message-item').length,visibleDialogCount:[...document.querySelectorAll('[role="dialog"]')].filter(visible).length,detailFooterAbsent:!dialog.querySelector('.ant-modal-footer'),createOrReplyNeverInvoked:true})
  cur.checks.push({label:'native Header X returns focus to exact still-connected enabled original detail opener',category:'primary-close-focus',qualified:opener.isConnected&&!opener.disabled&&opener.tabIndex>=0,passed:document.activeElement===opener})
  cur.checks.push({label:'original detail route and closed thread/overlay state observed without writes',passed:router.currentRoute.value.path==='/adapter/tickets'&&!visible(dialog)&&document.querySelectorAll('.message-item').length===0})
  cur.checks.push({label:'only exact allowlisted GETs within original route-return reread bound',passed:calls.length>=3&&calls.length<=4&&calls.every(c=>c.method==='get')&&calls.filter(c=>c.url==='/adapter-service/tickets').length>=1&&calls.filter(c=>c.url==='/adapter-service/tickets').length<=2&&['/adapter-service/public/register-status','/adapter-service/tickets/8094'].every(url=>calls.filter(c=>c.url===url).length===1)})
  cur.checks.push({label:'zero clipboard/real IO/runtime/console errors',passed:!realIO.length&&!runtime.length&&!errors.length})
  cur.completedFlow=true;assert.deepEqual(cur.checks.filter(c=>!c.passed).map(c=>c.label),[],'complete-tail current native Ticket detail contract; no create/reply/state action');cur.passed=true
 }finally{write()}
})

async function guardDetail(w,router){
 const opener=button(w.element.querySelector('tr[data-row-key="8094"]'),'查看详情');await click(opener)
 await until(()=>router.currentRoute.value.path==='/adapter/tickets/8094'&&calls.slice(activePlan.start).some(c=>c.url==='/adapter-service/tickets/8094'),'real native detail route/read before close guard')
 const dialog=await until(()=>[...document.querySelectorAll('.ant-modal')].find(n=>visible(n)&&compact(n.querySelector('.ant-modal-title')?.textContent)===ROW.subject&&n.querySelector('.message-item p')?.textContent===THREAD.body),'real readonly detail for close guard');await until(()=>!entering(dialog),'original detail transition settled for guard');await settle();return{w,router,opener,dialog}
}
function holdOriginalCloseRoute(router){
 const gate={entered:false,released:false,enteredFrom:null,enteredTo:null,completed:false,failureType:null,completionPath:null};let release
 const promise=new Promise(r=>{release=r});const removeBefore=router.beforeEach((to,from)=>{if(!gate.entered&&to.path==='/adapter/tickets'&&from.path==='/adapter/tickets/8094'){gate.entered=true;gate.enteredFrom=from.path;gate.enteredTo=to.path;return promise}})
 const removeAfter=router.afterEach((to,from,failure)=>{if(gate.entered&&to.path==='/adapter/tickets'&&from.path==='/adapter/tickets/8094'){gate.completed=true;gate.failureType=failure?.type??null;gate.completionPath=router.currentRoute.value.path}})
 return{gate,release(){if(!gate.released){gate.released=true;release()}},remove(){removeBefore();removeAfter()}}
}
function focusInput(mode){const input=document.createElement('input');input.id='fixture-owned-'+mode;input.setAttribute('aria-label','Synthetic foreground focus owner');document.body.append(input);input.focus();assert.equal(document.activeElement,input,'native standalone foreground focus qualification');return input}
function guardCheck(label,passed,qualified=true){cur.checks.push({label,category:'guard',qualified,passed:qualified?Boolean(passed):null})}
test('TC02 original readonly close navigation preserves foreground/dialog and retired actor-role/logout/page focus',{timeout:90000},async()=>{
 cur={name:'TC02',completedFlow:false,passed:false,ioBase:calls.length,checks:[],observations:[],vectors:[]};cases.push(cur)
 try{
  for(const mode of ['foreground','other-dialog','actor','role-ABA','logout','unmount','other-route']){
   activePlan={start:calls.length,actorB:mode==='actor'};const vector={mode,completedFlow:false,qualification:{},observations:[]};cur.vectors.push(vector);let r,e,g,input,other,expectedFocus,unmounted=false
   try{
    r=await appPage();e=await guardDetail(r.w,r.router);g=holdOriginalCloseRoute(r.router)
    const close=e.dialog.querySelector('button.ant-modal-close');assert.ok(close&&visible(close),'original native header close for real router gate');await click(close)
    await until(()=>g.gate.entered&&!visible(e.dialog),'actual original close navigation reaches held beforeEach and dialog closes')
    vector.qualification={realNavigationGateEntered:g.gate.entered,heldFrom:g.gate.enteredFrom,heldTo:g.gate.enteredTo,heldCurrentPath:r.router.currentRoute.value.path,oldDialogDisconnected:!e.dialog.isConnected,openerConnected:e.opener.isConnected,openerEnabled:!e.opener.disabled}
    guardCheck(mode+' actual original close reaches pending router navigation before restoration',g.gate.entered&&r.router.currentRoute.value.path==='/adapter/tickets/8094'&&!visible(e.dialog))
    guardCheck(mode+' no premature opener return while close route is unresolved',document.activeElement!==e.opener)
    if(mode==='foreground'){input=focusInput(mode);expectedFocus=input}
    else if(mode==='other-dialog'){
     other=Antd.Modal.info({title:'SYNTHETIC_FOREIGN_DIALOG',content:'Synthetic readonly foreign focus owner',wrapProps:{'aria-modal':true}})
     const foreign=await until(()=>[...document.querySelectorAll('.ant-modal')].find(n=>visible(n)&&compact(n.querySelector('.ant-modal-confirm-title')?.textContent)==='SYNTHETIC_FOREIGN_DIALOG'),'other real Antd dialog visible');await until(()=>!entering(foreign),'other actual dialog natural transition');await settle();const role=foreign.closest('[role="dialog"]')||foreign.querySelector('[role="dialog"]');assert.ok(role?.contains(document.activeElement),'real other dialog settled focus qualification');expectedFocus=document.activeElement;vector.qualification.otherRealAntdDialog=true
    }else if(mode==='actor'){
     user.setStoredCurrentUser({...ACTOR,id:1906202,username:'synthetic_ticket_actor_b'});await until(()=>calls.slice(activePlan.start).filter(c=>c.url==='/adapter-service/tickets').length===2&&calls.slice(activePlan.start).filter(c=>c.url==='/adapter-service/tickets/8094').length===2,'new enabled actor legitimate exact list-empty/detail404 reads settle');await settle();input=focusInput(mode);expectedFocus=input;vector.qualification.newActorVisible=Number(user.getStoredCurrentUser()?.id)===1906202
    }else if(mode==='role-ABA'){
     user.setStoredCurrentUser({...ACTOR,role:'SYNTHETIC_NONADMIN'});vector.qualification.roleLiteralStored=user.getStoredCurrentUser()?.role==='SYNTHETIC_NONADMIN';user.setStoredCurrentUser({...ACTOR});await settle();vector.qualification.roleReturnedToUser=user.getStoredCurrentUser()?.role==='user';vector.qualification.originalOpenerStillConnected=e.opener.isConnected;vector.qualification.activeBodyBeforeRelease=document.activeElement===document.body
     guardCheck('role-only nonadmin literal ABA accepted without identity/adminness or DOM-disconnect masking',vector.qualification.roleLiteralStored&&vector.qualification.roleReturnedToUser&&e.opener.isConnected&&document.activeElement===document.body);expectedFocus=document.body
    }else if(mode==='logout'){user.clearStoredCurrentUser();await settle();input=focusInput(mode);expectedFocus=input;vector.qualification.explicitLogout=!user.getStoredCurrentUser()}
    else if(mode==='unmount'){const own=roots.find(x=>x.router===r.router);own.app.unmount();unmounted=true;await settle();input=focusInput(mode);expectedFocus=input;vector.qualification.actualAppUnmounted=true}
    else if(mode==='other-route'){await r.router.push('/fixture-other');await until(()=>r.router.currentRoute.value.path==='/fixture-other','actual later route supersedes old pending close');input=focusInput(mode);expectedFocus=input;vector.qualification.actualOtherRoute=true}
    vector.observations.push({label:'before-original-close-navigation-release',focusTag:document.activeElement?.tagName||null,isOldOpener:document.activeElement===e.opener,oldOpenerConnected:e.opener.isConnected,route:r.router.currentRoute.value.path})
    g.release();await until(()=>g.gate.completed,'public afterEach observes matching original close navigation completion or cancellation');await until(()=>r.router.currentRoute.value.path===(mode==='unmount'?'/':mode==='other-route'?'/fixture-other':'/adapter/tickets'),'actual final route respects last-App unmount START_LOCATION or superseding other route');await settle();await settle()
    guardCheck(mode+' original pending close never steals newer or retired focus',document.activeElement===expectedFocus&&document.activeElement!==e.opener)
    vector.observations.push({label:'after-route-completion-and-Vue-disposal',matchingCloseAfterEach:g.gate.completed,closeNavigationFailureType:g.gate.failureType,closeAfterEachCurrentPath:g.gate.completionPath,focusTag:document.activeElement?.tagName||null,focusPreserved:document.activeElement===expectedFocus,isOldOpener:document.activeElement===e.opener,oldOpenerConnected:e.opener.isConnected,route:r.router.currentRoute.value.path,connectedTicketThreads:document.querySelectorAll('.message-item').length,createNeverOpened:true})
    vector.calls=calls.slice(activePlan.start);vector.completedFlow=true
   }finally{
    if(g){g.release();g.remove()}if(other)other.destroy();await settle()
    if(r){const own=roots.find(x=>x.router===r.router);if(own){if(!unmounted)own.app.unmount();roots.splice(roots.indexOf(own),1)}r.router.options.history.destroy()}
    input?.remove();await settle();Antd.Modal.destroyAll();Antd.message.destroy();user.clearStoredCurrentUser();localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle()
   }
  }
  guardCheck('all seven guarded readonly vectors completed with zero business writes/real IO/runtime errors',cur.vectors.length===7&&cur.vectors.every(v=>v.completedFlow)&&!realIO.length&&!runtime.length&&!errors.length)
  cur.completedFlow=true;assert.deepEqual(cur.checks.filter(q=>q.qualified!==false&&!q.passed).map(q=>q.label),[],'original close guard contract');cur.passed=true
 }finally{write()}
})

afterEach(async()=>{try{for(const r of roots.splice(0)){r.app.unmount();r.router.options.history.destroy()}await settle();Antd.Modal.destroyAll();Antd.message.destroy();user.clearStoredCurrentUser();localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle();cur.io={calls:calls.slice(cur.ioBase||0),writes:calls.filter(c=>c.method!=='get').length,copies:[],realIO:[...realIO],runtimeCount:runtime.length,consoleErrorCount:errors.length,consoleWarningCount:warnings.length};cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(errors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}finally{write()}})
after(()=>{Swal.fire=oldSwalFire;axios.defaults.adapter=originalAdapter;console.error=oldError;console.warn=oldWarn;write()})
