/* Actual Tiku page/selection/modal + Antd zhCN + real API/request/Axios.
 * Public question rows and all effects are synthetic; read-search POST is not a business write. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
assert.ok(path.isAbsolute(web),'absolute source web required')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}}
globalThis.getComputedStyle=window.getComputedStyle
for(const key of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','CustomEvent'])globalThis[key]=window[key]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,h}=require('vue'),{createRouter,createMemoryHistory,RouterView}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const zhCN=require('ant-design-vue/lib/locale/zh_CN').default,user=require(path.join(web,'src/utils/currentUser.js'))
const ADMIN={id:67001,username:'SYNTHETIC_BULK_ADMIN',role:'admin',status:'enabled'},COURSE_A='SYNTHETIC_SUBMITTED_COURSE_A',COURSE_B='SYNTHETIC_UNSUBMITTED_COURSE_B',IDS=[701,702]
const cases=[],calls=[],pending=[],roots=[],realIO=[],runtime=[],consoleErrors=[],consoleWarnings=[],notices=[];let cur=null,rows=[]
const sha=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),compact=v=>String(v??'').replace(/\s+/g,' ').trim(),clone=v=>JSON.parse(JSON.stringify(v))
const inputNames=['src/pages/TikuComponent/index.vue','src/pages/TikuComponent/useQuestionReads.mjs','src/pages/TikuComponent/useQuestionSelection.mjs','src/pages/TikuComponent/useQuestionDetail.mjs','src/pages/TikuComponent/useQuestionEditor.mjs','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/requestHelpers.mjs','src/utils/requestRetry.mjs','src/utils/topicOverviewHelpers.mjs','src/utils/currentUser.js','src/utils/currentUserScope.js','src/composables/useTableColumnPreferences.mjs','src/components/OverflowTableText.vue','src/components/RequestFailureAlert.vue','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputNames.map(p=>[p,sha(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=sha(__filename)
const check=(value,label)=>cur.checks.push({label,passed:!!value})
function initialRows(){return IDS.map((id,index)=>({id,question:'SYNTHETIC_QUESTION_'+id,type:index,options:JSON.stringify(['SYNTHETIC_OPTION_A_'+id,'SYNTHETIC_OPTION_B_'+id]),answer:JSON.stringify(['SYNTHETIC_OPTION_A_'+id]),hash:(index?'b':'a').repeat(32),course_name:'SYNTHETIC_ORIGINAL_COURSE_'+id,extra:'SYNTHETIC_LOW_FREQUENCY_TAG_'+id,source_name:'SYNTHETIC_IMPORTED_SOURCE_'+id}))}
function sameRow(a,b){const x=Object.keys(a).sort(),y=Object.keys(b).sort();return x.length===y.length&&x.every((key,i)=>key===y[i]&&Object.is(a[key],b[key]))}
const originalAdapter=axios.defaults.adapter
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url),body=typeof config.data==='string'?JSON.parse(config.data):config.data,call={method,path:url,actor:user.getStoredCurrentUser()?.id||null};calls.push(call)
 let response
 if(method==='post'&&url==='/adapter-service/questions/search'){
  call.kind='question-read-search';call.queryFields=Object.keys(body||{}).sort();call.pageNo=body?.pageNo;call.pageSize=body?.pageSize
  response={items:clone(rows),total:rows.length,pageNo:body?.pageNo||0,pageSize:body?.pageSize||10}
 }else if(method==='get'&&url==='/adapter-service/courses'){
  call.kind='course-read';response=['SYNTHETIC_ORIGINAL_COURSE_701','SYNTHETIC_ORIGINAL_COURSE_702']
 }else if(method==='put'&&IDS.some(id=>url==='/adapter-service/questions/'+id)){
  call.kind='question-update';call.id=body?.id;call.fieldNames=Object.keys(body||{}).sort();call.payloadUsesSubmittedA=body?.course_name===COURSE_A
  const expected={...rows.find(r=>r.id===body?.id),course_name:COURSE_A}
  call.fullPublicRowPreserved=sameRow(body||{},expected)
  call.lowFrequencyPreserved=['options','answer','hash','extra','source_name'].every(key=>Object.is(body?.[key],expected[key]))
  const captured=clone(body);let resolve
  const promise=new Promise(yes=>{resolve=yes}),item={call,promise,settled:false,finish:(status=200)=>{assert.ok(!item.settled,'actual PUT resolves once');item.settled=true;call.status=status;if(status===200)rows=rows.map(row=>row.id===captured.id?{...captured}:row);resolve({status,data:{},headers:{'x-request-id':calls.length.toString(16).padStart(32,'0')},config,request:{memoryOnly:true}})}};pending.push(item);return promise
 }else{realIO.push('unexpected-api-operation');return Promise.reject(new Error('FORBIDDEN_API_OPERATION'))}
 return Promise.resolve({status:200,data:response,headers:{'x-request-id':calls.length.toString(16).padStart(32,'0')},config,request:{memoryOnly:true}})
}
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_REAL_IO_'+kind)}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const p=require(name);p.request=()=>deny(name);p.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(String(e.error?.stack||e.message)));window.addEventListener('unhandledrejection',e=>runtime.push(String(e.reason?.stack||e.reason)))
const originalConsoleError=console.error,originalConsoleWarn=console.warn,originalMessages={}
console.error=function(...args){consoleErrors.push(args.map(x=>String(x?.stack||x)).join(' '));return Reflect.apply(originalConsoleError,this,args)}
console.warn=function(...args){consoleWarnings.push(args.map(x=>String(x?.stack||x)).join(' '));return Reflect.apply(originalConsoleWarn,this,args)}
for(const level of ['success','error','warning','info']){originalMessages[level]=Antd.message[level];Antd.message[level]=function(...args){const text=String(args[0]?.content||args[0]||'');notices.push({level,successForOne:text==='成功更新 1 条题目的课程名称',successForTwo:text==='成功更新 2 条题目的课程名称',firstRowFailure:text==='题目 701 更新课程名称失败'});return Reflect.apply(originalMessages[level],this,args)}}
const Page=require(path.join(web,'src/pages/TikuComponent/index.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(predicate,label){const end=Date.now()+7000;while(Date.now()<end){if(predicate())return;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=window.getComputedStyle(p);if(p.hidden||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled,'actual visible enabled native click');n.focus?.();n.click();await settle()}
function button(root,label){const list=[...root.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent).replace(/\s/g,'')===label);assert.equal(list.length,1,'one scoped native '+label+' button');return list[0]}
function liveModals(){return[...document.querySelectorAll('.ant-modal')].filter(n=>visible(n)&&compact(n.querySelector('.ant-modal-title')?.textContent)==='批量编辑课程名称')}
async function modal(){await until(()=>{const count=liveModals().length;assert.ok(count<=1,'no duplicate visible bulk-course modal during admission');return count===1},'actual bulk-course modal ready');assert.equal(liveModals().length,1);return liveModals()[0]}
function courseInput(m){const inputs=[...m.querySelectorAll('input[role="combobox"]')];assert.equal(inputs.length,1,'actual native AutoComplete input');return inputs[0]}
async function typeCourse(m,value){const n=courseInput(m);assert.ok(!n.disabled&&!n.readOnly,'same-owner course input remains editable during original PUT');n.focus();n.value=value;n.dispatchEvent(new Event('input',{bubbles:true}));n.dispatchEvent(new Event('change',{bubbles:true}));await settle();return n}
const readCount=()=>calls.filter(c=>c.kind==='question-read-search').length,writeCount=()=>calls.filter(c=>c.kind==='question-update').length,courseCount=()=>calls.filter(c=>c.kind==='course-read').length
function view(w,expectedDraft){return{modalOpen:w.vm.bulkCourseVisible,draftLength:String(w.vm.bulkCourseName||'').length,draftEqualsExpected:w.vm.bulkCourseName===expectedDraft,selectedIDs:[...w.vm.selectedRowKeys],selectedCacheCount:w.vm.selectedQuestions.length,currentRowsWithWrittenA:w.vm.data.filter(r=>r.course_name===COURSE_A).map(r=>r.id),selectedCacheWithWrittenA:w.vm.selectedQuestions.filter(r=>r.course_name===COURSE_A).map(r=>r.id),listLoading:w.vm.tabLoading,visibleBulkModalCount:liveModals().length,readSearchPOSTs:readCount(),courseGETs:courseCount(),updatePUTs:writeCount()}}
async function open(){
 rows=initialRows();user.setStoredCurrentUser(ADMIN);const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/tiku',component:Page}]});await router.push('/adapter/tiku');await router.isReady()
 const Host={render:()=>h(Antd.ConfigProvider,{locale:zhCN},{default:()=>h(RouterView)})},app=mount(Host,{attachTo:document.body,global:{plugins:[Antd,router],config:{errorHandler:e=>runtime.push(String(e?.stack||e))}}});roots.push({app,router});await settle();const w=app.findComponent(Page);assert.ok(w.exists(),'actual Tiku parent and selection modules')
 await until(()=>!w.vm.tabLoading&&w.vm.data.length===2&&w.vm.courses.length===2,'valid public questions and course suggestions')
 check(app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn','actual zhCN provider')
 check(IDS.every(id=>!!w.element.querySelector('tr[data-row-key="'+id+'"]'))&&w.vm.selectedRowKeys.length===0,'actual native public question rows render with no initial selection')
 return w
}
async function select(w,ids){for(const id of ids){const n=w.element.querySelector('tr[data-row-key="'+id+'"] input[type="checkbox"]');assert.ok(n,'actual row selection checkbox');await click(n)}check(JSON.stringify([...w.vm.selectedRowKeys])===JSON.stringify(ids),'native row checkbox selection establishes exact intended IDs')}
async function begin(w,ids=IDS.slice(0,1)){
 await select(w,ids);await click(button(w.element,'批量编辑课程名'));const m=await modal();await typeCourse(m,'  '+COURSE_A+'  ');const before={reads:readCount(),courses:courseCount(),writes:writeCount(),successes:notices.filter(n=>n.level==='success').length}
 await click(button(m.querySelector('.ant-modal-footer'),'保存'));await until(()=>writeCount()===before.writes+1&&pending.some(p=>!p.settled),'first original PUT genuinely pending')
 const item=pending.at(-1);check(item.promise instanceof Promise&&!item.settled&&item.call.fullPublicRowPreserved&&item.call.lowFrequencyPreserved&&item.call.payloadUsesSubmittedA,'held actual PUT uses exact public full row with low-frequency fields and effective submitted A')
 return{m,item,before}
}
async function finish(item,status=200){item.finish(status);await settle()}
async function closeCurrent(){const owned=roots.pop();owned.app.unmount();owned.router.options.history.destroy();await settle();user.clearStoredCurrentUser();window.localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle()}
function writeLedger(){if(process.env.BULK_COURSE_LEDGER)fs.writeFileSync(process.env.BULK_COURSE_LEDGER,JSON.stringify({schema:'TIKU_BULK_COURSE_NATIVE_LEDGER_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),cases,nativeDOM:true,physicalBrowser:false,actualPageAPIRequestAxiosMemory:true,noRawQuestionAnswerCourseFullFormPayloadRecorded:true,realProviderDBAndServerAuthorizationNotQualified:true},null,2)+'\n')}
function register(name,fn){test(name,{timeout:25000},async()=>{cur={name,checks:[],observations:[],passed:false};cases.push(cur);try{await fn();check(realIO.length===0&&runtime.length===0&&consoleErrors.length===0,'zero real IO/runtime/console errors');const failed=cur.checks.filter(c=>!c.passed);assert.equal(failed.length,0,failed.map(c=>c.label).join('; '));cur.passed=true}catch(error){cur.error={name:error.name,message:String(error.message)};throw error}finally{writeLedger()}})}
afterEach(async()=>{if(!cur)return;try{while(roots.length)await closeCurrent();for(const item of pending)if(!item.settled)item.finish(503);await settle();Antd.message.destroy();cur.io={calls:[...calls],readSearchPOSTs:readCount(),courseGETs:courseCount(),updatePUTs:writeCount(),realIO:[...realIO],runtime:[...runtime],consoleErrors:[...consoleErrors],consoleWarnings:[...consoleWarnings],notices:[...notices]};cur.pendingProof=pending.map(p=>({id:p.call.id,actualPromise:p.promise instanceof Promise,settled:p.settled,status:p.call.status??null}));cur.cleanup={bodyEmpty:document.body.childElementCount===0,allPendingSettled:pending.every(p=>p.settled),sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(consoleErrors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}catch(error){cur.passed=false;cur.hookError={name:error.name,message:String(error.message)};throw error}finally{writeLedger();calls.length=pending.length=realIO.length=runtime.length=consoleErrors.length=consoleWarnings.length=notices.length=0;cur=null}})
after(()=>{axios.defaults.adapter=originalAdapter;console.error=originalConsoleError;console.warn=originalConsoleWarn;for(const level of Object.keys(originalMessages))Antd.message[level]=originalMessages[level];writeLedger()})

register('BC01 completed A keeps genuinely new same-dialog B or cleared draft plus selected IDs, while preserving A success and exactly original one readback',async()=>{
 for(const variant of ['new-B','cleared']){
  const w=await open(),{m,item,before}=await begin(w),draft=variant==='new-B'?COURSE_B:'',input=await typeCourse(m,draft)
  check(w.vm.bulkCourseVisible&&w.vm.bulkCourseName===draft&&input.value===draft&&!input.disabled&&!input.readOnly&&!item.settled,'actual enabled same modal contains new raw draft while original A PUT is still pending: '+variant)
  await finish(item);await until(()=>readCount()===before.reads+1&&!w.vm.tabLoading,'original successful A readback finishes')
  const current=liveModals()[0]
  check(w.vm.bulkCourseVisible&&w.vm.bulkCourseName===draft&&!!current&&visible(input)&&input.value===draft,'A success does not close or erase current new/cleared native draft: '+variant)
  check(JSON.stringify([...w.vm.selectedRowKeys])===JSON.stringify([IDS[0]])&&w.vm.selectedQuestions.length===1,'new draft intended selected context remains after A success: '+variant)
  check(w.vm.data.find(r=>r.id===IDS[0])?.course_name===COURSE_A&&w.vm.selectedQuestions.find(r=>r.id===IDS[0])?.course_name===COURSE_A,'original successful readback truthfully updates written A row and selected cache, not unsubmitted B: '+variant)
  check(writeCount()===before.writes+1&&readCount()===before.reads+1&&courseCount()===before.courses&&notices.filter(n=>n.level==='success').length===before.successes+1,'A executes once/succeeds/readbacks once; B is never automatically submitted: '+variant)
  cur.observations.push({label:variant,state:view(w,draft),actualInputMatchesDraft:input.value===draft});await closeCurrent()
 }
})

register('BC02 unchanged, trim-equivalent and effective-return-to-A retain original close/reset/single readback; raw formatting does not invent an ABA policy',async()=>{
 for(const variant of ['unchanged','trim-equivalent','return-A']){
  const w=await open(),{m,item,before}=await begin(w)
  if(variant==='trim-equivalent')await typeCourse(m,' '+COURSE_A+' ')
  else if(variant==='return-A'){await typeCourse(m,COURSE_B);await typeCourse(m,'   '+COURSE_A+'   ')}
  check(String(w.vm.bulkCourseName||'').trim()===COURSE_A&&!item.settled,'current effective draft still equals submitted A while pending: '+variant)
  await finish(item);await until(()=>readCount()===before.reads+1&&!w.vm.tabLoading,'normal all-success readback finishes')
  check(!w.vm.bulkCourseVisible&&w.vm.bulkCourseName===''&&w.vm.selectedRowKeys.length===0,'original close/name-reset/selection-reset preserved for effective same A: '+variant)
  check(writeCount()===before.writes+1&&readCount()===before.reads+1&&courseCount()===before.courses&&notices.filter(n=>n.level==='success').length===before.successes+1,'normal A success emits original single PUT/readback/success: '+variant)
  cur.observations.push({label:variant,state:view(w,'')});await closeCurrent()
 }
})

register('BC03 two selected public rows keep original strict serial full-row PUT payload and all low-frequency fields, then one aggregate readback',async()=>{
 const w=await open(),{item:first,before}=await begin(w,IDS)
 check(pending.filter(p=>!p.settled).length===1&&writeCount()===before.writes+1&&readCount()===before.reads,'second PUT and readback cannot start before first response')
 await finish(first);await until(()=>writeCount()===before.writes+2,'second serial PUT starts only after first200');const second=pending.at(-1)
 check(first.settled&&!second.settled&&second.call.id===IDS[1]&&readCount()===before.reads,'second actual request is pending after first and no speculative read occurs')
 check(pending.slice(-2).every(p=>p.call.fullPublicRowPreserved&&p.call.lowFrequencyPreserved&&p.call.payloadUsesSubmittedA)&&pending.slice(-2).map(p=>p.call.id).join(',')===IDS.join(','),'both serial payloads preserve every public row field/options/answer/hash/extra/source_name with only course replaced')
 await finish(second);await until(()=>readCount()===before.reads+1&&!w.vm.tabLoading,'aggregate all-success readback')
 check(!w.vm.bulkCourseVisible&&w.vm.bulkCourseName===''&&w.vm.selectedRowKeys.length===0&&w.vm.data.every(r=>r.course_name===COURSE_A),'normal two-row success closes/resets and readback truthfully shows written A')
 check(writeCount()===before.writes+2&&readCount()===before.reads+1&&courseCount()===before.courses&&notices.some(n=>n.level==='success'&&n.successForTwo),'exact original serial two PUTs/one success/one readback, no extra course read')
 cur.observations.push({label:'strict two-row serial success',state:view(w,'')})
})

register('BC04 first non200 and retired old dialog retain original stop/no-readback policy without letting old A erase a reopened B modal',async()=>{
 let w=await open(),work=await begin(w,IDS);const originalRaw=w.vm.bulkCourseName
 await finish(work.item,500)
 check(writeCount()===work.before.writes+1&&readCount()===work.before.reads&&pending.every(p=>p.settled),'first real non200 stops before second row with no retry or speculative readback')
 check(w.vm.bulkCourseVisible&&w.vm.bulkCourseName===originalRaw&&JSON.stringify([...w.vm.selectedRowKeys])===JSON.stringify(IDS)&&notices.some(n=>n.level==='error'&&n.firstRowFailure),'original non200 retains modal/raw A/selected IDs and error')
 await closeCurrent()
 w=await open();work=await begin(w,IDS);await click(button(work.m.querySelector('.ant-modal-footer'),'取消'));await click(button(w.element,'批量编辑课程名'));const fresh=await modal(),input=await typeCourse(fresh,COURSE_B)
 check(w.vm.bulkCourseVisible&&w.vm.bulkCourseName===COURSE_B&&!work.item.settled,'native close/reopen creates current B dialog while original A remains issued')
 const successBefore=notices.filter(n=>n.level==='success').length;await finish(work.item)
 check(writeCount()===work.before.writes+1&&readCount()===work.before.reads&&pending.every(p=>p.settled),'retired old dialog cannot dispatch remaining serial write or readback')
 check(w.vm.bulkCourseVisible&&w.vm.bulkCourseName===COURSE_B&&input.value===COURSE_B&&JSON.stringify([...w.vm.selectedRowKeys])===JSON.stringify(IDS)&&notices.filter(n=>n.level==='success').length===successBefore,'old owner completion cannot close/clear current B dialog, selection or claim aggregate success')
 cur.observations.push({label:'retired old dialog versus current B',state:view(w,COURSE_B)})
})
