/* Actual App/zhCN + production router guards/Layout/Menu + UserUsage/RechargeOrderList.
 * Only Axios server outcomes are inert. No server payment/persistence/Auth claim. */
const {test,afterEach,after}=require('node:test')
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
assert.ok(web&&path.isAbsolute(web),'explicit frozen source web required')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
const helper=require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));helper.setViewport(1440)
require.extensions['.css']=m=>{m.exports={}}
globalThis.getComputedStyle=window.getComputedStyle
for(const key of ['DOMParser','HTMLInputElement','HTMLButtonElement','HTMLDivElement','CustomEvent'])globalThis[key]=window[key]
if(!HTMLElement.prototype.scrollIntoView)HTMLElement.prototype.scrollIntoView=function(){}
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick}=require('vue'),Antd=require('ant-design-vue'),axios=require('axios')
const user=require(path.join(web,'src/utils/currentUser.js'))
const A={id:61001,username:'SYNTHETIC_PENDING_NORMAL_USER',role:'user',status:'enabled',balance:0}
const ORDER='SYNTHETIC_PENDING_202_ONLY',FOREIGN='SYNTHETIC_FOREIGN_NOT_VISIBLE'
const method={id:17,name:'SYNTHETIC 支付方式',enabled:true,sort:0,iconSVG:'',remark:'仅内存，不支付'}
const records=[],calls=[],roots=[],realIO=[],runtime=[],consoleErrors=[],notices=[];let cur=null
const sha=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),compact=v=>String(v??'').replace(/\s+/g,' ').trim()
const inputNames=['src/App.vue','src/router/router.js','src/layout/TikuAdapterLayout.vue','src/components/SiderMenu/index.vue','src/components/PageHeader/index.vue','src/pages/UserUsage/index.vue','src/pages/RechargeOrderList/index.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/requestHelpers.mjs','src/utils/requestRetry.mjs','src/utils/requestFailureView.mjs','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/moneyFormat.mjs','src/utils/userPortalHelpers.mjs','src/components/RequestFailureAlert.vue','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputNames.map(p=>[p,sha(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=sha(__filename)
const check=(value,label)=>cur.checks.push({label,passed:!!value})
const clone=v=>v===undefined?undefined:JSON.parse(JSON.stringify(v))
const kindOf=(method,url)=>method==='get'&&url==='/adapter-service/user/session'?'session':method==='get'&&url==='/adapter-service/public/register-status'?'public':method==='get'&&url==='/adapter-service/user/info'?'balance':method==='get'&&url==='/adapter-service/payment-methods'?'methods':method==='get'&&url==='/adapter-service/billing-records'?'billing':url==='/adapter-service/recharge-orders'&&method==='post'?'order-create':url==='/adapter-service/recharge-orders'&&method==='get'?'orders-read':'forbidden'
function pendingRow(foreign=false){return{id:foreign?502:501,userId:foreign?61002:A.id,username:foreign?'SYNTHETIC_FOREIGN':A.username,orderNo:foreign?FOREIGN:ORDER,paymentProvider:'epay',paymentMethodNameSnapshot:method.name,amount:12.34,status:'pending',thirdPartyOrderNo:'',paidAt:null,createdAt:'2026-11-01T00:00:00Z'}}
function orderBody(mode){const rows=mode==='own'?[pendingRow()]:mode==='mixed'?[pendingRow(),pendingRow(true)]:[];return{data:rows,total:rows.length,page:1,pageSize:10,summary:{totalAmount:rows.length*12.34,currentPageAmount:rows.length*12.34}}}
const previousAdapter=axios.defaults.adapter
axios.defaults.adapter=config=>{
 const methodName=String(config.method||'get').toLowerCase(),url=String(config.url),kind=kindOf(methodName,url)
 const data=typeof config.data==='string'?JSON.parse(config.data):config.data,params=clone(config.params||{}),key=config.headers?.get?.('Idempotency-Key')||config.headers?.['Idempotency-Key']
 const call={kind,method:methodName,url,params,data:clone(data),actor:user.getStoredCurrentUser()?.id||null,id:(calls.length+1).toString(16).padStart(32,'0'),idempotencyKeyPresent:typeof key==='string'&&!!key,idempotencyKeySHA256:typeof key==='string'?crypto.createHash('sha256').update(key).digest('hex'):null}
 calls.push(call)
 if(kind==='forbidden'){realIO.push('unexpected-api-operation');return Promise.reject(new Error('FORBIDDEN_API_OPERATION'))}
 assert.ok(cur,'API belongs to an active native case')
 const mode=cur.mode;let status=200,body
 if(kind==='session'){assert.equal(user.getStoredCurrentUser()?.id,A.id,'heartbeat belongs only to the legitimate protected synthetic actor');assert.ok(config.signal&&config.data==null&&config.params===undefined,'original bodyless cancellable heartbeat GET');body={message:'ok'}}
 if(kind==='public')body={siteName:'Synthetic Pending Flow',registerEnabled:true}
 if(kind==='balance'){status=mode.balanceStatus;body={...A,balance:mode.balance,minRechargeAmount:1}}
 if(kind==='methods'){status=mode.methodsStatus;body=mode.methodsRows}
 if(kind==='billing'){status=mode.billingStatus;body={data:clone(mode.billingRows),total:mode.billingTotal,page:1,pageSize:10,summary:{totalAmount:mode.billingRows.reduce((s,r)=>s+r.amount,0),currentPageAmount:mode.billingRows.reduce((s,r)=>s+r.amount,0)}}}
 if(kind==='order-create'){status=202;body={orderNo:ORDER,status:'pending',fixtureOutcomeOnly:true}}
 if(kind==='orders-read'){status=mode.ordersStatus;body=orderBody(mode.ordersMode)}
 if(status!==200&&status!==202)body={message:'SYNTHETIC_TARGETED_READ_FAILURE',code:'SYNTHETIC_READ_ONLY'}
 call.status=status;call.responseShape=Array.isArray(body)?'array':body&&typeof body==='object'?Object.keys(body).sort():typeof body
 return Promise.resolve({status,data:body,headers:{'x-request-id':call.id},config,request:{memoryOnly:true}})
}
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_REAL_IO_'+kind)}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open')
globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(String(e.error?.stack||e.message)));window.addEventListener('unhandledrejection',e=>runtime.push(String(e.reason?.stack||e.reason)))
const originalConsoleError=console.error,originalConsoleWarn=console.warn
console.error=function(...args){consoleErrors.push(args.map(x=>String(x?.stack||x)).join(' '));return Reflect.apply(originalConsoleError,this,args)}
console.warn=function(...args){notices.push(args.map(x=>String(x)).join(' '));return Reflect.apply(originalConsoleWarn,this,args)}
const App=require(path.join(web,'src/App.vue')),Usage=require(path.join(web,'src/pages/UserUsage/index.vue')),Orders=require(path.join(web,'src/pages/RechargeOrderList/index.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,12));await nextTick();await flushPromises()}
async function until(predicate,label){const deadline=Date.now()+10000;while(Date.now()<deadline){if(predicate())return;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=window.getComputedStyle(p);if(p.hidden||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
function button(root,label){const found=[...root.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent).replace(/\s/g,'')===label);assert.equal(found.length,1,'one actual native button '+label);return found[0]}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled,'actual visible enabled native click');n.focus?.();n.click();await settle()}
function type(n,value){assert.ok(n&&!n.disabled&&!n.readOnly,'actual editable input');n.focus();n.value=value;n.dispatchEvent(new Event('input',{bubbles:true}));n.dispatchEvent(new Event('change',{bubbles:true}))}
const count=kind=>calls.filter(c=>c.kind===kind).length
function statValue(w,title){const card=[...w.element.querySelectorAll('.summary-card')].find(n=>compact(n.querySelector('.ant-statistic-title')?.textContent)===title);assert.ok(card,'actual statistic '+title);return compact(card.querySelector('.ant-statistic-content')?.textContent)}
const numeric=v=>/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$/.test(String(v).replace(/[¥,\s]/g,''))
async function open(){user.setStoredCurrentUser({...A,balance:cur.mode.seedBalance});window.history.replaceState({},'', '/adapter/user-usage');const file=path.join(web,'src/router/router.js');delete require.cache[require.resolve(file)];const router=require(file).default;assert.ok(router?.options?.history,'actual production router exported');await router.push('/adapter/user-usage');await router.isReady();const app=mount(App,{attachTo:document.body,global:{plugins:[Antd,router],config:{errorHandler:e=>runtime.push(String(e?.stack||e))}}});roots.push({app,router});await settle();await until(()=>app.findComponent(Usage).exists(),'actual production route renders UserUsage');const w=app.findComponent(Usage);await until(()=>!w.vm.currentUserLoading&&!w.vm.paymentMethodLoading&&!w.vm.tableLoading,'initial real API reads settle');check(app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn','actual App ConfigProvider supplies zhCN');check(!!app.element.querySelector('nav[aria-label="用户导航"]'),'actual normal-user SiderMenu/Layout rendered');return{app,router,w}}
function recordObservation(label,w){cur.observations.push({label,billingError:w.vm.billingLoadError,balanceError:w.vm.balanceRefreshError,billingRows:w.vm.dataSource.length,billingRemoteTotal:statValue(w,'账单总数'),visiblePageAmount:statValue(w,'当前页消费金额'),emptyTexts:[...w.element.querySelectorAll('.ant-empty-description')].map(n=>compact(n.textContent)),balanceText:compact(w.element.querySelector('.balance-value')?.textContent)})}
function writeLedger(){if(process.env.U06C_PENDING_LEDGER)fs.writeFileSync(process.env.U06C_PENDING_LEDGER,JSON.stringify({schema:'U06C_PENDING_NATIVE_LEDGER_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),cases:records,nativeDOM:true,physicalBrowser:false,actualAppRouterLayoutMenuAPIRequestAxiosMemory:true,serverPersistencePaymentOrBackendAuthQualified:false},null,2)+'\n')}
function register(name,fn){test(name,{timeout:30000},async()=>{cur={name,checks:[],observations:[],passed:false,mode:{seedBalance:0,balance:0,balanceStatus:200,methodsStatus:200,methodsRows:[method],billingStatus:200,billingRows:[],billingTotal:0,ordersStatus:200,ordersMode:'empty'}};records.push(cur);try{await fn();check(count('session')===1,'single-active protected initial heartbeat is counted independently of all existing billing/order reads');check(realIO.length===0&&runtime.length===0&&consoleErrors.length===0,'no real network/runtime/console errors');const bad=cur.checks.filter(c=>!c.passed);assert.equal(bad.length,0,bad.map(c=>c.label).join('; '));cur.passed=true}catch(error){cur.error={name:error.name,message:String(error.message)};throw error}finally{writeLedger()}})}
afterEach(async()=>{if(!cur)return;try{for(const{app,router}of roots.splice(0)){app.unmount();router.options.history.destroy()}Antd.message.destroy();await settle();user.clearStoredCurrentUser();window.localStorage.clear();document.body.replaceChildren();await settle();cur.io={calls:clone(calls),actualCreatePOSTs:count('order-create'),realIO:[...realIO],runtime:[...runtime],consoleErrors:[...consoleErrors],consoleWarnings:[...notices]};cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(consoleErrors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}catch(error){cur.passed=false;cur.hookError={name:error.name,message:String(error.message)};throw error}finally{writeLedger();calls.length=realIO.length=runtime.length=consoleErrors.length=notices.length=0;cur=null}})
after(()=>{axios.defaults.adapter=previousAdapter;console.error=originalConsoleError;console.warn=originalConsoleWarn;writeLedger()})

register('PF01 initial GET failure is not successful empty or invented prior success; native retries recover known empty and low balance without creating orders',async()=>{
 Object.assign(cur.mode,{balanceStatus:503,billingStatus:503,methodsStatus:503});const{w}=await open()
 check(['balance','billing','methods'].every(k=>count(k)>=1&&count(k)<=3),'initial actual reads remain within1..3 owner-lease-permitted attempts')
 check(!!w.element.querySelector('.balance-refresh-error')&&!!w.element.querySelector('.billing-load-error')&&!!w.element.querySelector('.payment-method-load-error'),'all three first failures have their actual targeted recovery entry')
 recordObservation('first billing failure without previous success',w)
 check(w.element.textContent.includes('账单尚未加载成功')&&!w.element.textContent.includes('当前还没有消费记录。'),'initial failed table uses error-empty state, not successful empty')
 check(!w.vm.billingLoadError.includes('上次成功'),'initial billing failure does not invent retained previous success')
 check(!numeric(statValue(w,'账单总数')),'initial remote billing total is unknown, not invented known zero')
 check(w.vm.summary.currentPageCount===0&&w.vm.summary.totalAmount===0,'visible empty row count/sum can remain0; not treated as remote total proof')
 check(w.vm.currentUserBalance===0&&!w.element.querySelector('.balance-warning'),'failed balance read retains login cache, does not assert known low-balance warning')
 const disabled=button(w.element,'创建充值订单');check(disabled.disabled,'unavailable payment methods disable order creation');disabled.click();await settle();check(count('order-create')===0,'disabled native click cannot create an order')
 cur.mode.methodsStatus=200;await click(button(w.element,'重试支付方式'));await until(()=>!w.vm.paymentMethodLoading&&!w.vm.paymentMethodsError&&w.vm.paymentMethods.length===1,'native payment retry recovers enabled method')
 check(!button(w.element,'创建充值订单').disabled&&w.vm.selectedPaymentMethodId===17,'payment recovery restores selected enabled method, no new balance rule')
 cur.mode.balanceStatus=200;cur.mode.balance=0;await click(button(w.element,'重试余额刷新'));await until(()=>!w.vm.currentUserLoading&&!w.vm.balanceRefreshError,'native balance retry succeeds at zero')
 check(!!w.element.querySelector('.balance-warning')&&!button(w.element,'创建充值订单').disabled,'known zero balance warns but does not prohibit recharge')
 cur.mode.billingStatus=200;await click(button(w.element,'重试账单加载'));await until(()=>!w.vm.tableLoading&&!w.vm.billingLoadError,'native billing retry succeeds empty')
 recordObservation('successful empty billing',w)
 check(w.element.textContent.includes('当前还没有消费记录。')&&w.vm.summary.total===0&&w.vm.summary.currentPageCount===0&&w.vm.summary.totalAmount===0,'successful empty billing is Chinese, known0 and non-error')
 cur.mode.balance=0.000062;cur.mode.billingRows=[{id:701,userId:A.id,type:'normal_search',direction:'expense',provider:'local',amount:0.000062,questionText:'SYNTHETIC_OWN_BILL',requestId:'synthetic-own-bill',createdAt:'2026-11-01T00:00:00Z'}];cur.mode.billingTotal=7
 await click(button(w.element,'刷新'));await until(()=>!w.vm.pageRefreshing&&w.vm.dataSource.length===1&&w.vm.currentUserBalance===0.000062,'native refresh publishes micro balance and nonzero prior good bill')
 check(compact(w.element.querySelector('.balance-value')?.textContent).includes('0.000062')&&w.vm.summary.totalAmount===0.000062,'six-place nonzero precision is retained in actual balance and bill sum')
 check(!w.element.querySelector('.balance-warning')&&!button(w.element,'创建充值订单').disabled,'positive micro balance is not <=0 warning and recharge remains available')
 cur.mode.billingStatus=503;await click(button(w.element,'刷新'));await until(()=>!w.vm.pageRefreshing&&!!w.vm.billingLoadError,'read fails after actual previous success')
 recordObservation('read failure after nonzero previous success',w)
 check(w.vm.dataSource.length===1&&w.vm.pagination.total===7&&w.vm.summary.totalAmount===0.000062&&w.vm.billingLoadError.includes('上次成功'),'later failure truly preserves previous good row/remote total/sum')
 check(count('order-create')===0&&calls.every(c=>c.actor===A.id),'read recovery never creates payment/order and keeps actual owner identity')
})

register('PF02 one inert keyed202 stays warning; actual user menu and guarded route read pending by keyword, fail closed on foreign mix, and recover by explicit GET only',async()=>{
 cur.mode.balance=0.000062;cur.mode.seedBalance=0.000062;const{app,router,w}=await open();const oldUid=w.vm.$.uid
 check(w.vm.dataSource.length===0&&!w.vm.billingLoadError&&w.element.textContent.includes('当前还没有消费记录。'),'initial successful empty billing is distinct from failed read')
 const amount=w.element.querySelector('.recharge-form .ant-input-number-input');type(amount,'12.34');amount.blur();await settle();await until(()=>Number(w.vm.rechargeAmount)===12.34,'actual amount input publishes payload')
 await click(button(w.element,'创建充值订单'));await until(()=>!w.vm.rechargeSubmitting&&count('order-create')===1,'one inert create completes202')
 const post=calls.find(c=>c.kind==='order-create');check(post.status===202&&JSON.stringify(post.data)===JSON.stringify({amount:12.34,paymentMethodId:17})&&post.idempotencyKeyPresent,'actual API/request POST carries exact amount/method and an actual keyed header')
 check(router.currentRoute.value.path==='/adapter/user-usage'&&count('orders-read')===0,'202 does not invent automatic navigation or record read')
 check([...document.querySelectorAll('.ant-message-warning')].some(n=>n.textContent.includes('支付平台结果待核对')&&n.textContent.includes(ORDER)&&n.textContent.includes('勿重复下单')),'actual202 warning retains order reference and caution')
 check(!document.body.textContent.includes('充值订单创建成功')&&!w.vm.paymentResultVisible&&w.vm.paymentJumpUrl===''&&w.vm.paymentQRCodeDataUrl==='','202 is not success, checkout, QR generation or window open')
 const nav=app.element.querySelector('nav[aria-label="用户导航"]');const menu=[...nav.querySelectorAll('[role="menuitem"]')].find(n=>compact(n.textContent)==='充值记录');assert.ok(menu,'actual normal-user menu includes recharge records');await click(menu)
 await until(()=>router.currentRoute.value.path==='/adapter/recharge-orders'&&app.findComponent(Orders).exists(),'actual production guards/menu route renders RechargeOrderList');const orders=app.findComponent(Orders);await until(()=>!orders.vm.tableLoading&&orders.vm.hasLoaded,'route initial owned GET succeeds')
 check(w.vm.$.isUnmounted&&orders.vm.$.uid!==oldUid&&!orders.vm.isAdmin,'actual old page destroyed and normal-user record page mounted')
 const selector=orders.element.querySelector('.filter-form .ant-select-selector');assert.ok(selector);selector.dispatchEvent(new MouseEvent('mousedown',{bubbles:true,cancelable:true,button:0}));await settle();await until(()=>[...document.querySelectorAll('.ant-select-item-option')].some(n=>visible(n)&&compact(n.textContent)==='待支付'),'actual pending option opens');const option=[...document.querySelectorAll('.ant-select-item-option')].find(n=>visible(n)&&compact(n.textContent)==='待支付');await click(option);await until(()=>orders.vm.filters.status==='pending'&&!orders.vm.tableLoading,'native pending selection publishes readonly query')
 const keyword=orders.element.querySelector('.filter-form .ant-input-search input');assert.ok(keyword);const beforeSearch=count('orders-read');type(keyword,ORDER);await settle();check(count('orders-read')===beforeSearch,'keyword typing is draft, not automatic GET')
 cur.mode.ordersStatus=403;await click(orders.element.querySelector('.ant-input-search-button'));await until(()=>!orders.vm.tableLoading&&!!orders.vm.loadFailure,'explicit native keyword GET returns403');const failed=calls.filter(c=>c.kind==='orders-read').at(-1)
 check(failed.params.keyword===ORDER&&failed.params.status==='pending'&&count('order-create')===1,'explicit GET uses current orderNo/pending and never repeats POST')
 check(user.getStoredCurrentUser()?.id===A.id&&router.currentRoute.value.path==='/adapter/recharge-orders','actual403 read preserves session/route, not backend permission proof')
 cur.mode.ordersStatus=200;cur.mode.ordersMode='own';await click(orders.element.querySelector('[data-action="retry-request"]'));await until(()=>!orders.vm.tableLoading&&!orders.vm.loadFailure&&orders.vm.dataSource.length===1,'explicit failure retry reads scripted own pending record')
 const own=orders.element.querySelector('tr[data-row-key="501"]');check(!!own&&own.textContent.includes(ORDER)&&own.textContent.includes('待支付')&&orders.vm.summary.total===1&&orders.vm.summary.totalAmount===12.34,'actual pending row/owned summary render; scripted outcome is not payment confirmation')
 cur.mode.ordersMode='mixed';await click(button(orders.element,'刷新'));await until(()=>!orders.vm.tableLoading&&!!orders.vm.loadFailure,'mixed own/foreign read completes failclosed')
 check(orders.vm.dataSource.length===0&&orders.vm.pagination.total===0&&orders.vm.summary.total==='--'&&orders.vm.summary.totalAmount==='--'&&orders.vm.summary.currentPageAmount==='--','explicit foreign row clears ALL rows and aggregates, not partial filtering')
 check(!orders.element.textContent.includes(FOREIGN)&&!orders.element.querySelector('tr[data-row-key="501"]'),'no foreign row or previously visible own row leaks after rejected mixed response')
 cur.mode.ordersMode='own';await click(orders.element.querySelector('[data-action="retry-request"]'));await until(()=>!orders.vm.tableLoading&&!orders.vm.loadFailure&&orders.vm.dataSource.length===1,'explicit GET retry restores owned pending display')
 check(count('order-create')===1&&calls.filter(c=>c.kind==='orders-read').every(c=>c.method==='get')&&calls.every(c=>c.actor===A.id),'all reconciliation is owned GET, original create remains exactly1')
 cur.syntheticBoundary={createdByServerProven:false,paidProven:false,backendAuthProven:false,inertPOST202:1,pendingGETScripted:true,actualMenuRoute:true,productionRouterGuards:true}
})
