/* Actual AI page/controlled children + Antd zhCN + real API/request/Axios.
 * Synthetic config and held inert GETs only; never record connection/key values or a full form. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
assert.ok(path.isAbsolute(web),'absolute source web required')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}}
globalThis.getComputedStyle=window.getComputedStyle
for(const key of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','CustomEvent'])globalThis[key]=window[key]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,h}=require('vue'),{createRouter,createMemoryHistory,RouterView}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const zhCN=require('ant-design-vue/lib/locale/zh_CN').default,user=require(path.join(web,'src/utils/currentUser.js'))
const A={id:66001,username:'SYNTHETIC_AI_ADMIN_A',role:'admin',status:'enabled',balance:1},B={id:66002,username:'SYNTHETIC_AI_ADMIN_B',role:'admin',status:'enabled',balance:2}
const cases=[],calls=[],pending=[],roots=[],realIO=[],runtime=[],consoleErrors=[],consoleWarnings=[],notices=[],sensitive=new Set();let cur=null
const sha=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),compact=v=>String(v??'').replace(/\s+/g,' ').trim(),effectiveUrl=v=>String(v||'').trim().replace(/\/+$/,'')
const inputNames=['src/pages/AISearchConfig/index.vue','src/pages/AISearchConfig/AIConnectionModelSection.vue','src/pages/AISearchConfig/AIPromptSettingsSection.vue','src/pages/AISearchConfig/AIAdvancedSettingsSection.vue','src/pages/AISearchConfig/AISearchTestDialog.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/requestHelpers.mjs','src/utils/requestRetry.mjs','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/useUnsavedDraft.js','src/utils/aiSearchModelHelpers.mjs','src/utils/listSearchHelpers.mjs','src/components/RequestFailureAlert.vue','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputNames.map(p=>[p,sha(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=sha(__filename)
const check=(value,label)=>cur.checks.push({label,passed:!!value})
function remember(value){sensitive.add(String(value));sensitive.add(effectiveUrl(value));sensitive.add(String(value).trim());return value}
function config(actor){const tag=actor?.id===B.id?'B':'A';return{enabled:true,baseUrl:remember('https://synthetic-ai-'+tag.toLowerCase()+'.invalid/v1'),apiKey:remember('SYNTHETIC_ONLY_AI_KEY_'+tag+'_NOT_A_CREDENTIAL'),model:'MODEL-'+tag,manualModels:['MODEL-'+tag],prompt:'SYNTHETIC_PROMPT',remark:'',temperature:0,topP:0,maxAISearchModelCalls:4,maxAISearches:3,maxAISearchRetries:3,searchPricePerCall:.000062,maxAIWebReads:5,webReadPricePerCall:1.234567,enableAIStagedTokenBilling:true,smoothAIStagedTokenMultiplier:true,stagedTokenMultiplierPoints:[{tokens:'0',multiplier:2},{tokens:'1000',multiplier:1}]}}
function safeText(value){let text=String(value);for(const v of sensitive)if(v)text=text.split(v).join('[SYNTHETIC_CONNECTION_REDACTED]');return text.split('\n').slice(0,3).join('\n').slice(0,800)}
const originalAdapter=axios.defaults.adapter
axios.defaults.adapter=c=>{
 const method=String(c.method||'get').toLowerCase(),url=String(c.url),actor=user.getStoredCurrentUser(),synthetic=config(actor),params=c.params||{},call={method,path:url,actor:actor?.id||null,paramFields:Object.keys(params).sort(),hasUrl:!!params.url,hasKey:!!params.key};calls.push(call)
 if(method!=='get'||!['/adapter-service/ai-search-config','/adapter-service/ai-model-prices','/adapter-service/ai-search-config/models','/adapter-service/ai-search-config/ocr-cache'].includes(url)){realIO.push('unexpected-api-operation');return Promise.reject(new Error('FORBIDDEN_API_OPERATION'))}
 if(url==='/adapter-service/ai-search-config/models'){
  call.urlMatchesInitialSynthetic=effectiveUrl(synthetic.baseUrl)===params.url;call.keyMatchesInitialSynthetic=synthetic.apiKey.trim()===params.key
  let finish;const promise=new Promise(resolve=>{finish=resolve}),item={call,promise,settled:false,finish:models=>{assert.ok(!item.settled,'held actual HTTP response resolves once');item.settled=true;finish({status:200,data:{models},headers:{'x-request-id':calls.length.toString(16).padStart(32,'0')},config:c,request:{memoryOnly:true}})}};pending.push(item);return promise
 }
 if(url==='/adapter-service/ai-search-config/ocr-cache') assert.ok(c.errorFeedback==='local'&&Object.keys(params).length===0&&!c.data,'exact readonly OCR GET: local feedback, no payload')
 const body=url==='/adapter-service/ai-search-config/ocr-cache'?{configured:false,entries:0}:url==='/adapter-service/ai-search-config'?synthetic:[{id:50,modelName:synthetic.model,displayName:'Synthetic price',inputPrice:0,outputPrice:0,multiplier:1}]
 return Promise.resolve({status:200,data:body,headers:{'x-request-id':calls.length.toString(16).padStart(32,'0')},config:c,request:{memoryOnly:true}})
}
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_REAL_IO_'+kind)}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const p=require(name);p.request=()=>deny(name);p.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(safeText(e.error?.stack||e.message)));window.addEventListener('unhandledrejection',e=>runtime.push(safeText(e.reason?.stack||e.reason)))
const originalConsoleError=console.error,originalConsoleWarn=console.warn,originalMessages={}
console.error=function(...args){const text=args.map(x=>safeText(x?.stack||x)).join(' ');consoleErrors.push(text);return Reflect.apply(originalConsoleError,this,[text])}
console.warn=function(...args){const text=args.map(x=>safeText(x?.stack||x)).join(' ');consoleWarnings.push(text);return Reflect.apply(originalConsoleWarn,this,[text])}
for(const level of ['success','error','warning','info']){originalMessages[level]=Antd.message[level];Antd.message[level]=function(...args){const raw=String(args[0]?.content||args[0]||''),containsSensitive=[...sensitive].some(v=>v&&raw.includes(v));notices.push({level,text:safeText(raw),modelFetchSuccess:level==='success'&&/^已获取 \d+ 个模型，保存后生效$/.test(raw),containsSensitive});return Reflect.apply(originalMessages[level],this,args)}}
const Page=require(path.join(web,'src/pages/AISearchConfig/index.vue')),Connection=require(path.join(web,'src/pages/AISearchConfig/AIConnectionModelSection.vue')),Prompt=require(path.join(web,'src/pages/AISearchConfig/AIPromptSettingsSection.vue')),Advanced=require(path.join(web,'src/pages/AISearchConfig/AIAdvancedSettingsSection.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(predicate,label){const end=Date.now()+7000;while(Date.now()<end){if(predicate())return;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=window.getComputedStyle(p);if(p.hidden||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled,'actual visible enabled native click');n.focus?.();n.click();await settle()}
function button(root,label){const a=[...root.querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent).replace(/\s/g,'')===label);assert.equal(a.length,1,'one scoped native '+label+' button');return a[0]}
function field(w,name,section=Connection){const child=w.findComponent(section);assert.ok(child.exists(),'actual controlled section');const n=document.getElementById(child.props('sectionId')+'-'+name);assert.ok(n&&w.element.contains(n),'actual native field '+name);return n}
async function type(w,name,value,section=Connection){const n=field(w,name,section);assert.ok(!n.disabled&&!n.readOnly,'current native '+name+' remains editable during fetch');if(['baseUrl','apiKey'].includes(name))remember(value);n.focus();n.value=value;n.dispatchEvent(new Event('input',{bubbles:true}));n.dispatchEvent(new Event('change',{bubbles:true}));await settle()}
function modelSuccessCount(){return notices.filter(n=>n.modelFetchSuccess).length}
function modelsCount(){return calls.filter(c=>c.path==='/adapter-service/ai-search-config/models').length}
function safeState(w){return{manualModelsText:w.vm.formState.manualModelsText,fetchingModels:w.vm.fetchingModels,configLoading:w.vm.tabLoading,pageNeedsRead:w.vm.pageNeedsConfigRead,dirty:w.vm.hasUnsavedChanges,connectionPresent:!!w.vm.formState.baseUrl,keyPresent:!!w.vm.formState.apiKey,model:w.vm.formState.model,modelSuccessNoticeCount:modelSuccessCount()}}
async function open(actor=A){
 const ocrReadStart=calls.filter(c=>c.path==='/adapter-service/ai-search-config/ocr-cache').length
 user.setStoredCurrentUser(actor);const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/ai-search-config',component:Page}]});await router.push('/adapter/ai-search-config');await router.isReady()
 const Host={render:()=>h(Antd.ConfigProvider,{locale:zhCN},{default:()=>h(RouterView)})},app=mount(Host,{attachTo:document.body,global:{plugins:[Antd,router],config:{errorHandler:e=>runtime.push(safeText(e?.stack||e))}}});roots.push({app,router});await settle();const w=app.findComponent(Page);assert.ok(w.exists(),'actual AI parent and controlled children')
 await until(()=>!w.vm.tabLoading&&!w.vm.priceLoading&&w.vm.canUseConfigActions()&&w.vm.formState.manualModelsText==='MODEL-'+(actor.id===B.id?'B':'A'),'valid config and pricing read')
 check(app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn','actual zhCN provider')
 check(modelsCount()===0||pending.every(p=>p.settled),'initial config/price entry does not automatically fetch models')
 assert.equal(calls.filter(c=>c.path==='/adapter-service/ai-search-config/ocr-cache').length,ocrReadStart+1,'one initial readonly OCR status GET per parent')
 return w
}
async function begin(w){const n=modelsCount();await click(button(w.findComponent(Connection).element,'获取远程模型列表'));await until(()=>modelsCount()===n+1&&pending.some(p=>!p.settled),'one actual pending model GET');const item=pending.at(-1);check(item.promise instanceof Promise&&!item.settled&&w.vm.fetchingModels,'actual model GET stays genuinely pending and original busy is true');return item}
async function finish(item,models){item.finish(models);await settle()}
function writeLedger(){if(process.env.AI_MODEL_DRAFT_LEDGER)fs.writeFileSync(process.env.AI_MODEL_DRAFT_LEDGER,JSON.stringify({schema:'AI_MODEL_DRAFT_NATIVE_LEDGER_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),cases,nativeDOM:true,physicalBrowser:false,actualPageAPIRequestAxiosMemory:true,noRawConnectionKeyOrFullFormRecorded:true,realProviderAndServerAuthorizationNotQualified:true},null,2)+'\n')}
function register(name,fn){test(name,{timeout:20000},async()=>{cur={name,checks:[],observations:[],passed:false};cases.push(cur);try{await fn();check(realIO.length===0&&runtime.length===0&&consoleErrors.length===0,'zero real IO/runtime/console errors');check(notices.every(n=>!n.containsSensitive),'no connection or key values exposed in notices');const failed=cur.checks.filter(c=>!c.passed);assert.equal(failed.length,0,failed.map(c=>c.label).join('; '));cur.passed=true}catch(error){cur.error={name:error.name,message:safeText(error.message)};throw error}finally{writeLedger()}})}
async function closeCurrent(){const owned=roots.pop();owned.app.unmount();owned.router.options.history.destroy();await settle();user.clearStoredCurrentUser();window.localStorage.clear();window.sessionStorage.clear();document.body.replaceChildren();await settle()}
afterEach(async()=>{if(!cur)return;try{while(roots.length)await closeCurrent();for(const item of pending)if(!item.settled)item.finish([]);await settle();Antd.message.destroy();Antd.Modal.destroyAll();cur.io={calls:[...calls],modelGETs:modelsCount(),writes:calls.filter(c=>c.method!=='get').length,realIO:[...realIO],runtime:[...runtime],consoleErrors:[...consoleErrors],consoleWarnings:[...consoleWarnings],notices:[...notices]};cur.pendingProof=pending.map(p=>({actualPromise:p.promise instanceof Promise,settled:p.settled,actor:p.call.actor}));cur.cleanup={bodyEmpty:document.body.childElementCount===0,allPromisesSettled:pending.every(p=>p.settled),sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(consoleErrors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}catch(error){cur.passed=false;cur.hookError={name:error.name,message:safeText(error.message)};throw error}finally{writeLedger();calls.length=pending.length=realIO.length=runtime.length=consoleErrors.length=consoleWarnings.length=notices.length=0;cur=null}})
after(()=>{axios.defaults.adapter=originalAdapter;console.error=originalConsoleError;console.warn=originalConsoleWarn;for(const level of Object.keys(originalMessages))Antd.message[level]=originalMessages[level];writeLedger()})

register('MF01 pending same-owner200 cannot replace a newer raw manual models draft or announce that old result as published; original owner finally still releases busy',async()=>{
 const w=await open(),item=await begin(w),draft='SYNTHETIC_NEW_MANUAL_DRAFT \n MODEL-X,MODEL-X ',count=modelsCount(),success=modelSuccessCount()
 await type(w,'manualModelsText',draft)
 check(w.vm.formState.manualModelsText===draft&&field(w,'manualModelsText').value===draft&&w.vm.fetchingModels,'native textarea makes an actual raw draft while original request remains pending')
 await finish(item,['MODEL-OLD-REMOTE']);await until(()=>!w.vm.fetchingModels,'original current-owner finally release')
 check(w.vm.formState.manualModelsText===draft&&field(w,'manualModelsText').value===draft,'late old200 preserves exact newer manual draft including formatting')
 check(modelSuccessCount()===success,'discarded old draft response does not falsely announce models published')
 check(modelsCount()===count&&!w.vm.fetchingModels&&w.vm.hasUnsavedChanges,'no automatic fetch/save/retry; draft stays dirty and original busy releases')
 cur.observations.push({label:'after old200 versus new manual draft',state:safeState(w),expectedDraftRetained:w.vm.formState.manualModelsText===draft})
})

register('MF02 unchanged fetch preserves original filter/dedup/feedback; equivalent outgoing source plus unrelated draft and balance edits must not retire a compatible response',async()=>{
 const w=await open();let item=await begin(w);const firstSuccess=modelSuccessCount()
 check(item.call.urlMatchesInitialSynthetic&&item.call.keyMatchesInitialSynthetic&&item.call.paramFields.join(',')==='key,url','original request uses exact existing effective synthetic url/key params, values not recorded')
 user.setStoredCurrentUser({...A,balance:99});await finish(item,['MODEL-NEW','MODEL-NEW','image-test','x-openai-compact'])
 await until(()=>!w.vm.fetchingModels,'normal fetch finally')
 check(w.vm.formState.manualModelsText==='MODEL-NEW'&&field(w,'manualModelsText').value==='MODEL-NEW'&&modelSuccessCount()===firstSuccess+1,'unedited manual/source plus balance metadata preserves original exact filter/dedup and success')
 item=await begin(w);const rawUrl=config(A).baseUrl+'///',rawKey='  '+config(A).apiKey+'  ',beforeSuccess=modelSuccessCount()
 await type(w,'baseUrl',rawUrl);await type(w,'apiKey',rawKey);await type(w,'model','MODEL-UNRELATED-DEFAULT');await type(w,'prompt','SYNTHETIC_UNRELATED_PROMPT',Prompt);await type(w,'maxAISearches','4',Advanced)
 check(effectiveUrl(w.vm.formState.baseUrl)===effectiveUrl(config(A).baseUrl)&&String(w.vm.formState.apiKey).trim()===config(A).apiKey,'raw cosmetic connection edits retain original effective outgoing source')
 await finish(item,['MODEL-COMPATIBLE','MODEL-COMPATIBLE','IMAGE-filtered','foo-openai-compact']);await until(()=>!w.vm.fetchingModels,'compatible response finally')
 check(w.vm.formState.manualModelsText==='MODEL-COMPATIBLE'&&modelSuccessCount()===beforeSuccess+1,'unchanged relevant manual/effective source accepts response despite unrelated draft edits')
 check(w.vm.formState.baseUrl===rawUrl&&w.vm.formState.apiKey===rawKey&&w.vm.formState.model==='MODEL-UNRELATED-DEFAULT'&&w.vm.formState.prompt==='SYNTHETIC_UNRELATED_PROMPT'&&Number(w.vm.formState.maxAISearches)===4,'compatible publication preserves cosmetic raw source and unrelated native edits')
 cur.observations.push({label:'normal and compatible positive controls',state:safeState(w),rawSourceRetained:true})
})

register('MF03 a changed effective endpoint or key independently prevents an old connection model list from attaching to the current draft; busy is not guarded by draft compatibility',async()=>{
 for(const name of ['baseUrl','apiKey']){
  const w=await open(),before=w.vm.formState.manualModelsText,item=await begin(w),success=modelSuccessCount(),count=modelsCount(),replacement=name==='baseUrl'?'https://synthetic-changed.invalid/v1':'SYNTHETIC_ONLY_CHANGED_KEY_NOT_A_CREDENTIAL'
  await type(w,name,replacement);check(w.vm.formState[name]===replacement&&w.vm.fetchingModels,'actual native effective source edit remains enabled while pending: '+name)
  await finish(item,['MODEL-FROM-OLD-CONNECTION']);await until(()=>!w.vm.fetchingModels,'source-changed original finally '+name)
  check(w.vm.formState.manualModelsText===before&&field(w,'manualModelsText').value===before,'old connection response does not replace current model draft: '+name)
  check(w.vm.formState[name]===replacement&&modelSuccessCount()===success&&modelsCount()===count,'new source is retained, stale success suppressed and no automatic external reread: '+name)
  cur.observations.push({label:'changed effective source '+name,state:safeState(w),connectionDraftRetained:true,oldModelListRejected:w.vm.formState.manualModelsText===before})
  await closeCurrent()
 }
})

register('MF04 an already issued retired A response cannot replace B draft or release B pending busy; current B response retains original model processing',async()=>{
 const w=await open(),a=await begin(w);user.setStoredCurrentUser(B);await settle()
 check(w.vm.pageNeedsConfigRead&&!w.vm.fetchingModels,'principal transition retires old fetch display and requires current config read')
 await click(button(w.element.querySelector('.section-actions'),'刷新'));await until(()=>!w.vm.tabLoading&&w.vm.canUseConfigActions()&&w.vm.formState.manualModelsText==='MODEL-B','explicit new-owner config read')
 const b=await begin(w),before=w.vm.formState.manualModelsText,noticeCount=notices.length
 await finish(a,['MODEL-RETIRED-A'])
 check(w.vm.fetchingModels&&!b.settled&&w.vm.formState.manualModelsText===before&&notices.length===noticeCount,'old A completion cannot mutate current B list/busy or feedback after actual API delivery')
 await finish(b,['MODEL-CURRENT-B','MODEL-CURRENT-B','image-ignored','x-openai-compact']);await until(()=>!w.vm.fetchingModels,'current B finally')
 check(w.vm.formState.manualModelsText==='MODEL-CURRENT-B'&&field(w,'manualModelsText').value==='MODEL-CURRENT-B'&&user.getStoredCurrentUser()?.id===B.id,'current B response publishes original filtered dedup list and keeps current identity')
 check(calls.every(c=>c.method==='get')&&modelsCount()===2,'exactly two explicit model GETs, no automatic provider test/save/replay')
 cur.observations.push({label:'current B after retired A settles',state:safeState(w)})
})
