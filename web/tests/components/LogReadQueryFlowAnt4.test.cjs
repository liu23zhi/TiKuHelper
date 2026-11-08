/* Actual LogList + Vue/Antd zhCN + RouterView/memory history + current API/request/Axios.
 * Server outcomes are inert public DTOs. No real network, backend authorization or physical-browser claim. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
assert.ok(path.isAbsolute(web),'absolute source web required')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}}
globalThis.getComputedStyle=window.getComputedStyle
for(const key of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','CustomEvent'])globalThis[key]=window[key]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,h}=require('vue'),{createRouter,createMemoryHistory,RouterView}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const zhCN=require('ant-design-vue/lib/locale/zh_CN').default,user=require(path.join(web,'src/utils/currentUser.js'))
const A={id:63001,username:'SYNTHETIC_LOG_QUERY_ADMIN',role:'admin',status:'enabled'},BKEY='SYNTHETIC_LOG_KEYWORD_B'
const cases=[],calls=[],plans=[],roots=[],realIO=[],runtime=[],consoleErrors=[],consoleWarnings=[];let cur=null
const sha=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),compact=v=>String(v??'').replace(/\s+/g,' ').trim(),clone=v=>v===undefined?undefined:JSON.parse(JSON.stringify(v))
const inputNames=['src/pages/LogList/index.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/requestHelpers.mjs','src/utils/requestRetry.mjs','src/utils/requestFailureView.mjs','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/logListHelpers.mjs','src/utils/listSearchHelpers.mjs','src/utils/locationHelpers.mjs','src/components/RequestFailureAlert.vue','src/components/OverflowTableText.vue','src/composables/useTableColumnPreferences.mjs','src/composables/useTableExpansion.mjs','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputNames.map(p=>[p,sha(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=sha(__filename)
const check=(value,label)=>cur.checks.push({label,passed:!!value})
function row(label){return{id:label==='A'?901:902,userId:A.id,username:A.username,module:'system',action:3,description:'SYNTHETIC_LOG_'+label+'_ROW',clientIp:'',qid:0,oldAnswer:'',newAnswer:'',createTime:'2026-11-01T00:00:00+08:00',billingIds:[]}}
function dto(label,params){return{data:label==='empty'?[]:[row(label)],total:label==='empty'?0:21,page:label==='empty'?1:Number(params.page)||1,pageSize:Number(params.pageSize)||10}}
const originalAdapter=axios.defaults.adapter
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url),params=clone(config.params||{}),plan=plans.shift()||{label:'A',status:200}
 const call={method,url,params,actor:user.getStoredCurrentUser()?.id||null,status:plan.status??null,outcome:plan.outcome||'response',label:plan.label||'A',id:(calls.length+1).toString(16).padStart(32,'0')};calls.push(call)
 if(method!=='get'||url!=='/adapter-service/logs'){realIO.push('unexpected-api-operation');return Promise.reject(new Error('FORBIDDEN_API_OPERATION'))}
 if(plan.outcome==='network')return Promise.reject(new axios.AxiosError('Network Error','ERR_NETWORK',config,{memoryOnly:true}))
 const status=plan.status??200;call.status=status
 return Promise.resolve({status,data:status===200?dto(call.label,params):{message:'当前账号无此操作权限。'},headers:{'x-request-id':call.id},config,request:{memoryOnly:true}})
}
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_REAL_IO_'+kind)}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(String(e.error?.stack||e.message)));window.addEventListener('unhandledrejection',e=>runtime.push(String(e.reason?.stack||e.reason)))
const originalConsoleError=console.error,originalConsoleWarn=console.warn
console.error=function(...args){consoleErrors.push(args.map(x=>String(x?.stack||x)).join(' '));return Reflect.apply(originalConsoleError,this,args)}
console.warn=function(...args){consoleWarnings.push(args.map(x=>String(x)).join(' '));return Reflect.apply(originalConsoleWarn,this,args)}
const Page=require(path.join(web,'src/pages/LogList/index.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(predicate,label){const deadline=Date.now()+7000;while(Date.now()<deadline){if(predicate())return;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=window.getComputedStyle(p);if(p.hidden||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled,'actual visible enabled native click');n.focus?.();n.click();await settle()}
function button(root,label){const list=[...root.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent).replace(/\s/g,'')===label);assert.equal(list.length,1,'one native button '+label);return list[0]}
function keyword(w){const n=w.element.querySelector('.filter-form .ant-input-search input');assert.ok(n,'actual keyword input');return n}
function type(n,value){assert.ok(n&&!n.disabled&&!n.readOnly);n.focus();n.value=value;n.dispatchEvent(new Event('input',{bubbles:true}));n.dispatchEvent(new Event('change',{bubbles:true}))}
function view(w){return{ids:w.vm.dataSource.map(r=>r.id),descriptions:w.vm.dataSource.map(r=>r.description),page:w.vm.pagination.current,pageSize:w.vm.pagination.pageSize,total:w.vm.pagination.total,keyword:w.vm.filters.keyword,loading:w.vm.tabLoading,error:w.vm.loadError,desktopText:compact(w.element.querySelector('.desktop-log-table')?.textContent),mobileText:compact(w.element.querySelector('.mobile-log-list')?.textContent),failureKind:w.element.querySelector('.request-failure')?.getAttribute('data-failure-kind')||null,diagnostic:w.element.querySelector('[data-request-diagnostic]')?.textContent||null}}
async function open(){
 user.setStoredCurrentUser(A)
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/logs',component:Page}]});await router.push('/adapter/logs');await router.isReady()
 const Shell={render:()=>h(Antd.ConfigProvider,{locale:zhCN},{default:()=>h(RouterView)})},app=mount(Shell,{attachTo:document.body,global:{plugins:[Antd,router],config:{errorHandler:e=>runtime.push(String(e?.stack||e))}}});roots.push({app,router});await settle()
 const w=app.findComponent(Page);assert.ok(w.exists(),'actual LogList via real RouterView');await until(()=>!w.vm.tabLoading&&w.vm.dataSource.some(r=>r.id===901),'valid A response publishes')
 check(app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn','actual zhCN ConfigProvider')
 check(calls.length===1&&JSON.stringify(calls[0].params)===JSON.stringify({page:1,pageSize:10})&&calls[0].actor===A.id,'original default owned GET reaches actual API/request/Axios once')
 check(w.vm.dataSource.length===1&&w.vm.pagination.total===21&&w.vm.pagination.current===1&&!w.vm.listFailure&&!w.vm.loadError,'valid public A DTO produces known row/total/page without failure')
 check(!!w.element.querySelector('tr[data-row-key="901"]')&&w.element.querySelector('.desktop-log-table').textContent.includes('SYNTHETIC_LOG_A_ROW')&&w.element.querySelector('.mobile-log-list').textContent.includes('SYNTHETIC_LOG_A_ROW'),'actual desktop/mobile DOM renders A summary')
 cur.observations.push({label:'known A200 positive',view:view(w)});return w
}
function writeLedger(){if(process.env.LOG_QUERY_LEDGER)fs.writeFileSync(process.env.LOG_QUERY_LEDGER,JSON.stringify({schema:'LOG_QUERY_NATIVE_LEDGER_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),cases,nativeDOM:true,physicalBrowser:false,actualPageAPIRequestAxiosMemory:true,backendAuthorizationAndRealProviderNotQualified:true},null,2)+'\n')}
function register(name,fn){test(name,{timeout:20000},async()=>{cur={name,checks:[],observations:[],passed:false};cases.push(cur);try{await fn();check(realIO.length===0&&runtime.length===0&&consoleErrors.length===0,'zero real IO/runtime/console errors');const failed=cur.checks.filter(c=>!c.passed);assert.equal(failed.length,0,failed.map(c=>c.label).join('; '));cur.passed=true}catch(error){cur.error={name:error.name,message:String(error.message)};throw error}finally{writeLedger()}})}
afterEach(async()=>{if(!cur)return;try{for(const{app,router}of roots.splice(0)){app.unmount();router.options.history.destroy()}await settle();Antd.message.destroy();user.clearStoredCurrentUser();window.localStorage.clear();document.body.replaceChildren();await settle();cur.io={calls:clone(calls),businessWrites:calls.filter(c=>c.method!=='get').length,realIO:[...realIO],runtime:[...runtime],consoleErrors:[...consoleErrors],consoleWarnings:[...consoleWarnings]};cur.cleanup={bodyEmpty:document.body.childElementCount===0,plansConsumed:plans.length===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(consoleErrors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}catch(error){cur.passed=false;cur.hookError={name:error.name,message:String(error.message)};throw error}finally{writeLedger();calls.length=plans.length=realIO.length=runtime.length=consoleErrors.length=consoleWarnings.length=0;cur=null}})
after(()=>{axios.defaults.adapter=originalAdapter;console.error=originalConsoleError;console.warn=originalConsoleWarn;writeLedger()})

register('LQ01 actual committed keyword B403 clears A rows/total and distinguishes failure from empty; explicit refresh recovers current B through same request pipeline',async()=>{
 const w=await open(),epoch=user.getCurrentUserLogoutEpoch(),before=calls.length
 type(keyword(w),'  '+BKEY+'  ');await settle()
 check(calls.length===before&&w.vm.filters.keyword===''&&w.vm.dataSource.some(r=>r.id===901),'raw keyword draft alone neither dispatches nor replaces published A')
 plans.push({status:403,label:'B'});await click(w.element.querySelector('.ant-input-search-button'));await until(()=>!w.vm.tabLoading&&!!w.vm.listFailure,'committed B403 handled')
 const failed=view(w),read=calls.at(-1);cur.observations.push({label:'native B403 failure',view:failed,response:{status:read.status,requestID:read.id},bodyText:compact(document.body.textContent)})
 check(read.status===403&&read.params.keyword===BKEY&&read.params.page===1&&failed.keyword===BKEY&&keyword(w).value===BKEY,'real native submit commits normalized B and page1')
 check(failed.ids.length===0&&failed.total===0&&!failed.desktopText.includes('SYNTHETIC_LOG_A_ROW')&&!failed.mobileText.includes('SYNTHETIC_LOG_A_ROW'),'failed B cannot present prior A rows/count as current B in desktop/mobile')
 check(failed.error==='获取操作日志失败，请重试。'&&!!w.element.querySelector('.request-failure')&&failed.desktopText.includes('加载未完成，请重试。')&&!w.element.textContent.includes('当前还没有日志记录'),'failed result is persistent failure-not-successful-empty')
 check(failed.diagnostic===read.id&&failed.failureKind==='forbidden','validated HTTP403 diagnostic ID and classifier survive real API/request')
 check(user.getStoredCurrentUser()?.id===A.id&&user.getCurrentUserLogoutEpoch()===epoch,'resolved403 follows existing client auth handling without logout; not backend auth proof')
 plans.push({status:200,label:'B'});await click(button(w.element,'刷新'));await until(()=>!w.vm.tabLoading&&!w.vm.listFailure&&w.vm.dataSource.some(r=>r.id===902),'explicit refresh recovers B')
 const recovered=view(w);cur.observations.push({label:'explicit same B GET200 recovery',view:recovered})
 check(calls.length===before+2&&calls.at(-1).params.keyword===BKEY&&calls.at(-1).params.page===1&&recovered.total===21,'recovery is exactly one explicit owned GET using committed B query')
 check(recovered.ids.length===1&&recovered.ids[0]===902&&!recovered.error&&!recovered.failureKind&&recovered.desktopText.includes('SYNTHETIC_LOG_B_ROW')&&recovered.mobileText.includes('SYNTHETIC_LOG_B_ROW'),'recovered current B row/total renders and retires prior failure')
 check(calls.every(c=>c.method==='get'&&c.url==='/adapter-service/logs'&&c.actor===A.id),'only exact owned read endpoint, no business mutation or location call')
})

register('LQ02 actual native page2 network rejection clears page1 and retains failed page intent; explicit diagnostic read retry reaches valid empty state without success/failure conflation',async()=>{
 const w=await open(),page2=w.element.querySelector('.desktop-log-table .ant-pagination-item-2 a');assert.ok(page2,'actual native page2 control from valid total21')
 const before=calls.length;plans.push(...Array.from({length:3},()=>({outcome:'network'})));await click(page2);await until(()=>!w.vm.tabLoading&&!!w.vm.listFailure,'page2 network rejection after bounded existing read attempts')
 const failed=view(w),attempts=calls.slice(before);cur.observations.push({label:'native page2 rejected read',view:failed,wireAttempts:clone(attempts)})
 check(attempts.length===3&&attempts.every(c=>c.outcome==='network'&&JSON.stringify(c.params)===JSON.stringify({page:2,pageSize:10})),'unchanged original read policy makes three bounded attempts for exactly committed native page2')
 check(failed.page===2&&failed.pageSize===10&&failed.ids.length===0&&failed.total===0&&!failed.desktopText.includes('SYNTHETIC_LOG_A_ROW')&&!failed.mobileText.includes('SYNTHETIC_LOG_A_ROW'),'network-failed page2 does not attribute old page1 rows/count to page2')
 check(failed.error==='获取操作日志失败，请重试。'&&failed.failureKind==='network'&&failed.desktopText.includes('加载未完成，请重试。')&&!w.element.textContent.includes('当前还没有日志记录'),'network failure remains explained persistent failure, not successful empty')
 check(!failed.diagnostic&&w.element.textContent.includes('本次未收到服务端诊断编号，不生成替代编号。'),'real no-response rejection cannot invent server diagnostic ID')
 const retry=w.element.querySelector('[data-action="retry-request"]');assert.ok(retry,'existing owned readonly retry CTA')
 plans.push({status:200,label:'empty'});await click(retry);await until(()=>!w.vm.tabLoading&&!w.vm.listFailure&&!w.vm.loadError&&w.vm.dataSource.length===0,'explicit GET retry returns valid empty DTO')
 const recovered=view(w);cur.observations.push({label:'explicit current page2 GET recovered with server empty page1',view:recovered,responseBody:dto('empty',calls.at(-1).params)})
 check(calls.length===before+4&&calls.at(-1).params.page===2&&calls.at(-1).params.pageSize===10,'read retry emits one GET for retained failed page intent and no automatic write')
 check(recovered.total===0&&recovered.page===1&&recovered.ids.length===0&&!recovered.error&&!recovered.failureKind&&recovered.desktopText.includes('当前还没有日志记录')&&recovered.mobileText.includes('当前还没有日志记录'),'valid empty server result accepts returned effective page1, Chinese empty text and no error')
 check(calls.every(c=>c.method==='get'&&c.url==='/adapter-service/logs'&&c.actor===A.id),'all retries/recovery remain exact owned readonly endpoint')
})
