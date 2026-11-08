/* Required integration input: actual Go httptest producer output, never fallback JSON. */
const {test,afterEach,after}=require('node:test')
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..'),inputPath=process.env.TIKU_METRICS_FLOW_INPUT
assert.ok(inputPath&&path.isAbsolute(inputPath),'TIKU_METRICS_FLOW_INPUT must identify an absolute actual producer JSON file')
assert.ok(fs.existsSync(inputPath),'actual producer JSON is missing; no synthetic default is permitted')
const inputBytes=fs.readFileSync(inputPath),inputSHA256=crypto.createHash('sha256').update(inputBytes).digest('hex'),envelope=JSON.parse(inputBytes)
assert.equal(envelope.schemaVersion,1,'supported producer envelope')
assert.ok(Array.isArray(envelope.cases),'actual producer cases')
function sourceCase(name){const cases=envelope.cases.filter(c=>c.name===name);assert.equal(cases.length,1,'unique producer '+name);const c=cases[0];assert.ok(c.headers&&typeof c.headers==='object');let body=c.body;if(typeof body==='string'){try{body=JSON.parse(body)}catch{assert.fail('producer case body must be actual valid JSON')}}assert.ok(body&&typeof body==='object'&&!Array.isArray(body));return{...c,decodedBody:body}}
const EMPTY=sourceCase('empty'),POPULATED=sourceCase('populated'),FORBIDDEN=sourceCase('forbidden')
assert.equal(EMPTY.status,200);assert.equal(POPULATED.status,200);assert.equal(FORBIDDEN.status,403)
const initialEnvelope=JSON.stringify(envelope)
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(path.join(web,'tests/helpers/vue-dom.cjs'))
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick}=require('vue'),Antd=require('ant-design-vue'),axios=require('axios'),user=require(path.join(web,'src/utils/currentUser.js'))
const ACTOR_A={id:82101,role:'admin',status:'enabled',username:'SYNTHETIC_METRICS_A'},ACTOR_B={id:82102,role:'admin',status:'enabled',username:'SYNTHETIC_METRICS_B'}
const calls=[],pending=[],pages=[],records=[],network=[],runtime=[];let plan=[],cur=null
const H=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')
const sourceNames=['src/pages/SystemSettings/SystemMetricsPanel.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/requestHelpers.mjs','src/utils/requestRetry.mjs','src/utils/requestFailureView.mjs','src/utils/currentUser.js','src/utils/currentUserScope.js','src/components/RequestFailureAlert.vue','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(sourceNames.map(n=>[path.join(web,n),H(path.join(web,n))])),sourceBefore=hashes()
const compact=v=>String(v||'').replace(/\s+/g,' ').trim(),check=(ok,label)=>cur.checks.push({label,passed:!!ok})
function wireHeaders(headers){return Object.fromEntries(Object.entries(headers).map(([key,value])=>[key.toLowerCase(),Array.isArray(value)?value.join(', '):String(value)]))}
const oldAdapter=axios.defaults.adapter
assert.equal(!!require.cache[path.join(web,'src/utils/request.js')],false,'install memory transport before actual request module')
axios.defaults.adapter=config=>{
  const method=String(config.method).toLowerCase(),url=String(config.url)
  if(method!=='get'||url!=='/adapter-service/system-metrics'){network.push('UNEXPECTED_OPERATION');return Promise.reject(new Error('UNEXPECTED_METRICS_OPERATION'))}
  const next=plan.shift();assert.ok(next,'every request must have an explicit actual producer case')
  const c=next.input,headers=wireHeaders(c.headers)
  calls.push({method,url,retryPolicy:config.retryPolicy,withCredentials:config.withCredentials,producerCase:c.name,status:c.status,bodySHA256:crypto.createHash('sha256').update(typeof c.body==='string'?c.body:JSON.stringify(c.body)).digest('hex'),originalHeaderKeys:Object.keys(c.headers).sort(),noInjectedHeaders:true})
  const response={status:c.status,data:c.body,headers,config,request:{memoryTransport:true,actualProducerInput:true}}
  if(next.deferred){let held;const promise=new Promise(resolve=>{held={response,resolve,done:false};pending.push(held)});held.promise=promise;return promise}
  return Promise.resolve(response)
}
function deny(name){network.push(name);throw new Error('FORBIDDEN_METRICS_FLOW_IO_'+name)}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(String(e.error?.message||e.message)));window.addEventListener('unhandledrejection',e=>runtime.push(String(e.reason?.message||e.reason)))
const Panel=require(path.join(web,'src/pages/SystemSettings/SystemMetricsPanel.vue'))
async function settle(){await nextTick();await flushPromises();await nextTick();await flushPromises()}
async function until(fn,label){const at=performance.now();while(performance.now()-at<2200){if(fn())return;await new Promise(r=>setTimeout(r,5));await settle()}assert.fail('native metrics wait '+label)}
function state(w,key){return w.vm[key]}
function field(w,name){const n=w.element.querySelector('[data-metric="'+name+'"]');assert.ok(n,'actual metric DOM '+name);return compact(n.textContent)}
function refresh(w){const b=w.element.querySelector('button[data-action="refresh-system-metrics"]');assert.ok(b&&!b.disabled,'actual native refresh button');b.click()}
async function open(actor=ACTOR_A){user.setStoredCurrentUser(actor);const w=mount(Panel,{attachTo:document.body,global:{plugins:[Antd],config:{errorHandler:e=>runtime.push(String(e?.message||e))}}});pages.push(w);await settle();return w}
function finish(held){assert.ok(held&&held.promise instanceof Promise&&!held.done,'actual admitted pending transport Promise');held.done=true;held.resolve(held.response)}
function countText(value){return value.toLocaleString('zh-CN')}
function assertSchemaPrivacy(body,label){
  check(Object.keys(body).sort().join(',')==='billing,cache,http,schemaVersion,startedAt,tasks,uptimeSeconds',label+' exact fixed top-level snapshot contains no private fields')
  for(const name of ['search','fallback']){
    check(Object.keys(body.cache[name]).sort().join(',')==='hits,misses,shared',label+' fixed low-cardinality cache dimensions '+name)
    check(Object.keys(body.tasks[name]).sort().join(',')==='failed,running,succeeded,total',label+' task counts expose no IDs/owners/status contents '+name)
  }
  check(Object.keys(body.billing).sort().join(',')==='attempts,failures,successes',label+' settlement events are not monetary/private fields')
}
function assertRendered(w,body,label){
  const paths={
    'http-total':body.http.total,'http-in-flight':body.http.inFlight,'http-failed':body.http.failed,'latency-count':body.http.latencyCount,
    'billing-attempts':body.billing.attempts,'billing-successes':body.billing.successes,'billing-failures':body.billing.failures,
  }
  for(const key of ['search','fallback']){
    for(const metric of ['hits','misses','shared'])paths['cache-'+key+'-'+metric]=body.cache[key][metric]
    for(const metric of ['total','running','succeeded','failed'])paths['tasks-'+key+'-'+metric]=body.tasks[key][metric]
  }
  for(const [name,value] of Object.entries(paths))check(field(w,name)===countText(value),label+' exact actual producer scalar '+name)
  check(field(w,'latency-mean')===(body.http.latencyCount?((body.http.latencyTotalMillis/body.http.latencyCount).toFixed(1)+' ms'):'暂无耗时样本'),label+' completed-sample mean, not started-request average')
  check(field(w,'latency-max')===(body.http.latencyCount?(body.http.latencyMaxMillis.toFixed(1)+' ms'):'暂无耗时样本'),label+' real empty/max latency semantics')
  check(w.text().includes('诊断事件，不是账务凭证/收入')&&w.text().includes('不提供排队容量或持久化历史')&&w.text().includes('当前保留任务'),label+' existing safe process-local/non-queue/non-ledger notice stays')
}
function register(name,run){test(name,async()=>{cur={name,passed:false,checks:[]};try{await run();assert.deepEqual(cur.checks.filter(c=>!c.passed).map(c=>c.label),[],name+' flow assertions');cur.passed=true}catch(e){cur.error={name:e.name,message:String(e.message)};throw e}finally{records.push(cur)}})}
function saveLedger(){if(!process.env.TIKU_METRICS_FLOW_LEDGER)return;const p=process.env.TIKU_METRICS_FLOW_LEDGER;assert.ok(path.isAbsolute(p)&&path.resolve(p)!==path.resolve(inputPath),'external optional ledger must not overwrite input');fs.mkdirSync(path.dirname(p),{recursive:true});fs.writeFileSync(p,JSON.stringify({schema:'SYSTEM_METRICS_ACTUAL_PRODUCER_NATIVE_FLOW_V1',inputPath,inputSHA256,producerSchemaVersion:envelope.schemaVersion,records,counts:{tests:records.length,pass:records.filter(x=>x.passed).length,fail:records.filter(x=>!x.passed).length},sourceBefore,sourceAfter:hashes(),sourceUnchanged:JSON.stringify(sourceBefore)===JSON.stringify(hashes()),scope:'Actual Go handler/middleware output through actual frontend API/request/Axios memory and real VueAntdPanel; no HTTP listen/DB/Provider or physical browser'},null,2)+'\n')}
afterEach(async()=>{
  for(const w of pages.splice(0))if(w.exists())w.unmount()
  for(const p of pending)if(!p.done)finish(p)
  await settle();Antd.message.destroy();Antd.Modal.destroyAll();user.clearStoredCurrentUser();window.localStorage.clear();document.body.replaceChildren();await settle()
  cur.io={calls:calls.map(c=>({...c})),network:[...network],runtime:[...runtime]};cur.cleanup={allPendingSettled:pending.every(p=>p.done),allPagesUnmounted:true,bodyEmpty:document.body.childElementCount===0,inputFileUnchanged:H(inputPath)===inputSHA256,inputObjectsUnchanged:JSON.stringify(envelope)===initialEnvelope,sourceUnchanged:JSON.stringify(sourceBefore)===JSON.stringify(hashes())}
  assert.deepEqual(network,[]);assert.deepEqual(runtime,[]);assert.ok(Object.values(cur.cleanup).every(Boolean));saveLedger();calls.length=pending.length=network.length=runtime.length=0;plan=[];cur=null
})
after(()=>{axios.defaults.adapter=oldAdapter;saveLedger();console.log('SYSTEM_METRICS_FLOW_RESULT '+JSON.stringify({inputSHA256,tests:records.length,pass:records.filter(c=>c.passed).length,fail:records.filter(c=>!c.passed).length,actualProducerInput:true,actualAPIRequest:true,nativeVueAntd:true,physicalBrowser:false}))})

register('FLOW01 actual empty then native refreshed populated handler snapshots render exact fixed-schema counts without private task fields',async()=>{
  assertSchemaPrivacy(EMPTY.decodedBody,'actual empty');assertSchemaPrivacy(POPULATED.decodedBody,'actual populated')
  check(EMPTY.decodedBody.http.total===1&&EMPTY.decodedBody.http.inFlight===1&&EMPTY.decodedBody.http.latencyCount===0,'empty includes its own real Go request start but no completed latency')
  check(Object.values(EMPTY.decodedBody.billing).every(n=>n===0)&&Object.values(EMPTY.decodedBody.cache).every(c=>Object.values(c).every(n=>n===0)),'real empty cache/settlement counters remain zero')
  for(const kind of ['search','fallback']){const t=POPULATED.decodedBody.tasks[kind];check(t.total>t.running+t.succeeded+t.failed,'actual unknown-status retained task is counted only in total '+kind)}
  plan=[{input:EMPTY},{input:POPULATED}];const w=await open();await until(()=>state(w,'metrics')&&!state(w,'loading'),'first actual empty render');assertRendered(w,EMPTY.decodedBody,'empty');check(calls.length===1&&calls[0].retryPolicy==='single','actual API sends one same-origin single-policy GET')
  refresh(w);await settle();await until(()=>!state(w,'loading')&&state(w,'metrics'),'native refreshed populated render');assertRendered(w,POPULATED.decodedBody,'populated');check(calls.length===2&&plan.length===0,'only mount and explicit native refresh GETs, no polling')
  check(w.element.querySelector('[data-metric="tasks-search-total"]').closest('div').querySelector('dt').textContent==='当前保留任务','retained total is not relabeled as queue length')
})

register('FLOW02 actual RequireAdmin403 travels real request/Auth without local logout and explicit retry consumes actual success snapshot',async()=>{
  plan=[{input:FORBIDDEN},{input:POPULATED}];const epoch=user.getCurrentUserLogoutEpoch(),href=window.location.href,w=await open();await until(()=>!state(w,'loading')&&state(w,'errorText'),'actual forbidden error UI')
  check(calls.length===1&&!state(w,'metrics'),'actual403 is not a successful zero snapshot or hidden retry')
  check(user.getStoredCurrentUser()?.id===ACTOR_A.id&&user.getCurrentUserLogoutEpoch()===epoch&&window.location.href===href,'actual frontend request/Auth403 does not logout or navigate')
  check(w.text().includes('无权读取运行诊断')&&w.element.querySelector('.request-failure')?.getAttribute('data-failure-kind')==='forbidden','actual403 has fixed persistent permission feedback')
  const retry=w.element.querySelector('button[data-action="retry-request"]');assert.ok(retry&&!retry.disabled,'actual current explicit read-retry control');retry.click();await settle();await until(()=>!state(w,'loading')&&state(w,'metrics'),'actual successful retry render');assertRendered(w,POPULATED.decodedBody,'after explicit retry');check(calls.length===2&&!state(w,'errorText'),'only explicit native retry recovers')
})

register('FLOW03 a genuine pending old read is retired by role/identity then cannot paint or release fresh manually-started B pending read',async()=>{
  plan=[{input:POPULATED,deferred:true},{input:EMPTY,deferred:true}];const w=await open();check(calls.length===1&&pending.length===1&&pending[0].promise instanceof Promise&&!pending[0].done&&state(w,'loading'),'old A has genuine admitted pending GET and busy')
  user.setStoredCurrentUser({...ACTOR_A,role:'user'});await settle();check(!w.find('.system-metrics-panel').exists()&&!state(w,'loading')&&!state(w,'metrics'),'actual demotion clears/hides old data and original busy');check(calls.length===1,'demotion does not automatically GET')
  user.setStoredCurrentUser(ACTOR_B);await settle();check(w.find('.system-metrics-panel').exists()&&!state(w,'loading'),'fresh admin B returns to explicit-read state');check(calls.length===1,'promotion/identity change does not automatically GET')
  refresh(w);await settle();check(calls.length===2&&pending.length===2&&pending[1].promise instanceof Promise&&!pending[1].done&&state(w,'loading'),'fresh B real native refresh creates second actual pending GET before old completion')
  finish(pending[0]);await settle();check(state(w,'loading')&&state(w,'metrics')===null&&!pending[1].done&&w.find('.metrics-loading').exists(),'old completion/finally cannot paint or release actual B pending state');check(calls.length===2,'old completion cannot replay GET')
  finish(pending[1]);await settle();await until(()=>!state(w,'loading')&&state(w,'metrics'),'B own actual empty completion');assertRendered(w,EMPTY.decodedBody,'fresh B only');check(calls.length===2,'bounded flow ends with exactly two explicit admitted reads')
})
