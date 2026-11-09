/* Actual SystemSettings/children + Vue/Antd zhCN + memory RouterView + real API/request.
 * Only public-shaped responses/keys are synthetic. Never emit raw secrets, form or request payload. */
const {test,afterEach,after}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),Module=require('node:module'),crypto=require('node:crypto')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
assert.ok(path.isAbsolute(web),'absolute source web required')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'));require.extensions['.css']=m=>{m.exports={}}
globalThis.getComputedStyle=window.getComputedStyle
for(const key of ['DOMParser','Document','HTMLInputElement','HTMLButtonElement','HTMLDivElement','CustomEvent'])globalThis[key]=window[key]
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick,h}=require('vue'),{createRouter,createMemoryHistory,RouterView}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
const zhCN=require('ant-design-vue/lib/locale/zh_CN').default,user=require(path.join(web,'src/utils/currentUser.js'))
const A={id:64001,username:'SYNTHETIC_SETTINGS_ADMIN_A',role:'admin',status:'enabled'},B={id:64002,username:'SYNTHETIC_SETTINGS_ADMIN_B',role:'admin',status:'enabled'},ordinary={id:64003,username:'SYNTHETIC_SETTINGS_USER',role:'user',status:'enabled'}
const groups=['站点与日志','认证与充值','商业化开关','敏感词检测','微信登录与验证码','归属地查询','支付回调','短信验证码配置'],secretFields=['epayPlatformPublicKey','epayMerchantPrivateKey','smsbaoSecret','wechatApiBearerToken','captchaApiKey','uapiApiKey'],privateFields=new Set(secretFields)
const cases=[],calls=[],roots=[],realIO=[],runtime=[],consoleErrors=[],consoleWarnings=[],secrets=new Set();let cur=null,expectedPayload=null,metricsSerial=0,savedNotices=0,restoreRemove=null
const sha=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),compact=v=>String(v??'').replace(/\s+/g,' ').trim()
const inputNames=['src/pages/SystemSettings/index.vue','src/pages/SystemSettings/SystemMetricsPanel.vue','src/pages/SystemSettings/PaymentMethodPanel.vue','src/pages/SystemSettings/components/PaymentMethodIconPicker.vue','src/api/TikuComponentApi.js','src/utils/request.js','src/utils/requestHelpers.mjs','src/utils/requestRetry.mjs','src/utils/currentUser.js','src/utils/currentUserScope.js','src/utils/useUnsavedDraft.js','src/utils/paymentMethodHelpers.mjs','src/utils/safePaymentIcon.mjs','src/components/SafePaymentIcon.js','src/components/RequestFailureAlert.vue','tests/helpers/vue-dom.cjs']
const hashes=()=>Object.fromEntries(inputNames.map(p=>[p,sha(path.join(web,p))])),sourceBefore=hashes(),fixtureSHA256=sha(__filename)
const check=(value,label)=>cur.checks.push({label,passed:!!value})
function syntheticSecret(tag,field){const v='SYNTHETIC_ONLY_'+tag+'_'+field+'_NOT_A_CREDENTIAL';secrets.add(v);return v}
function detail(tag){const v={siteName:'SYNTHETIC_SITE_'+tag,maxLogSizeBytes:2*1024*1024*1024,registerEnabled:true,newUserBonusAmount:.123456,minRechargeAmount:1.25,epayVersion:'v2',epayGatewayUrl:'https://synthetic-pay.invalid/submit.php',epayPartnerId:'SYNTHETIC_PID_'+tag,epayNotifyUrl:'https://synthetic-site.invalid/adapter-service/payment/notify',epayReturnUrl:'https://synthetic-site.invalid/adapter/recharge/result',smsProvider:'smsbao',smsbaoUsername:'SYNTHETIC_SMS_USER_'+tag,smsCodeSignName:'SYNTHETIC_SIGN',smsCodeTemplate:'SYNTHETIC {sign} {code}',smsLoginEnabled:true,wechatLoginEnabled:true,wechatScanLoginEnabled:true,wechatCodeLoginEnabled:true,wechatDefaultLoginMode:'code',wechatCodeStaticQrcodeUrl:'https://synthetic-static.invalid/qr.png',wechatApiBaseUrl:'https://synthetic-wechat.invalid',captchaProvider:'remote',captchaApiBaseUrl:'https://synthetic-captcha.invalid',captchaType:'ROTATE',smsbaoCredentialType:'password',smsbaoRegionMode:'domestic',rechargePromotionEnabled:false,deviceLimitEnabled:false,sensitiveWordCheckEnabled:false};for(const field of secretFields)v[field]=syntheticSecret(tag,field);return v}
function metrics(){return{schemaVersion:1,startedAt:'2026-11-01T00:00:00Z',uptimeSeconds:10,http:{total:++metricsSerial+6,inFlight:0,failed:0,latencyCount:0,latencyTotalMillis:0,latencyMaxMillis:0,buckets:[]},cache:{search:{hits:0,misses:0,shared:0},fallback:{hits:0,misses:0,shared:0}},billing:{attempts:0,successes:0,failures:0}}}
function safeText(v){let s=String(v);for(const value of secrets)s=s.split(value).join('[SYNTHETIC_SECRET_REDACTED]');return s.split('\n').slice(0,3).join('\n').slice(0,800)}
function secretState(state){return Object.fromEntries(secretFields.map(field=>[field,{present:typeof state[field]==='string'&&state[field].length>0,length:typeof state[field]==='string'?state[field].length:0}]))}
function sameObject(a,b){const x=Object.keys(a).sort(),y=Object.keys(b).sort();return x.length===y.length&&x.every((k,i)=>k===y[i]&&Object.is(a[k],b[k]))}
const originalAdapter=axios.defaults.adapter
axios.defaults.adapter=config=>{
 const method=String(config.method||'get').toLowerCase(),url=String(config.url),actor=user.getStoredCurrentUser(),call={method,url,actor:actor?.id||null,role:actor?.role||null};calls.push(call)
 const allowed=method==='get'&&['/adapter-service/system-settings','/adapter-service/payment-methods','/adapter-service/system-metrics'].includes(url)||method==='put'&&url==='/adapter-service/system-settings'
 if(!allowed){realIO.push('unexpected-api-operation');return Promise.reject(new Error('FORBIDDEN_API_OPERATION'))}
 let body
 if(method==='put'){
  const payload=typeof config.data==='string'?JSON.parse(config.data):config.data
  call.payloadKeys=Object.keys(payload).sort();call.secretSummary=secretState(payload);call.fullNormalizedPayloadMatches=!!expectedPayload&&sameObject(payload,expectedPayload);call.payloadFieldCount=Object.keys(payload).length
  call.emptyClearPreserved=payload.uapiApiKey==='';call.literalClearPreserved=payload.smsbaoSecret==='__CLEAR__';body={}
 }else if(url==='/adapter-service/system-settings')body=detail(actor?.id===B.id?'B':'A')
 else if(url==='/adapter-service/payment-methods')body=[]
 else body=metrics()
 return Promise.resolve({status:200,data:body,headers:{'x-request-id':calls.length.toString(16).padStart(32,'0')},config,request:{memoryOnly:true}})
}
function deny(kind){realIO.push(kind);throw new Error('FORBIDDEN_REAL_IO_'+kind)}
globalThis.fetch=window.fetch=()=>deny('fetch');window.XMLHttpRequest.prototype.open=function(){deny('xhr')};window.open=()=>deny('window-open');globalThis.WebSocket=window.WebSocket=class{constructor(){deny('websocket')}}
for(const name of ['node:http','node:https']){const m=require(name);m.request=()=>deny(name);m.get=()=>deny(name)}
window.addEventListener('error',e=>runtime.push(safeText(e.error?.stack||e.message)));window.addEventListener('unhandledrejection',e=>runtime.push(safeText(e.reason?.stack||e.reason)))
const originalConsoleError=console.error,originalConsoleWarn=console.warn,originalSuccess=Antd.message.success
console.error=function(...args){const value=args.map(x=>safeText(x?.stack||x)).join(' ');consoleErrors.push(value);return Reflect.apply(originalConsoleError,this,[value])}
console.warn=function(...args){const value=args.map(x=>safeText(x?.stack||x)).join(' ');consoleWarnings.push(value);return Reflect.apply(originalConsoleWarn,this,[value])}
Antd.message.success=function(...args){if(String(args[0]?.content||args[0]||'')==='保存成功')savedNotices++;return Reflect.apply(originalSuccess,this,args)}
const Page=require(path.join(web,'src/pages/SystemSettings/index.vue')),MetricsPanel=require(path.join(web,'src/pages/SystemSettings/SystemMetricsPanel.vue'))
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,10));await nextTick();await flushPromises()}
async function until(predicate,label){const deadline=Date.now()+7000;while(Date.now()<deadline){if(predicate())return;await settle()}assert.fail('native prerequisite: '+label)}
function visible(n){if(!n?.isConnected)return false;for(let p=n;p&&p.nodeType===1;p=p.parentElement){const s=window.getComputedStyle(p);if(p.hidden||p.getAttribute('aria-hidden')==='true'||s.display==='none'||s.visibility==='hidden')return false}return true}
async function click(n){assert.ok(n&&visible(n)&&!n.disabled,'actual visible enabled native click');n.focus?.();n.click();await settle()}
function button(w,label){const a=[...w.element.querySelector('.section-actions').querySelectorAll('button')].filter(n=>visible(n)&&compact(n.textContent).replace(/\s/g,'')===label);assert.equal(a.length,1,'one native '+label+' button');return a[0]}
function field(w,name){const n=document.getElementById(w.vm.fieldId(name));assert.ok(n&&w.element.contains(n),'actual native field '+name);return n}
function type(n,value){assert.ok(n&&!n.disabled&&!n.readOnly,'editable native input');n.focus();n.value=value;n.dispatchEvent(new Event('input',{bubbles:true}));n.dispatchEvent(new Event('change',{bubbles:true}))}
async function choose(w,name,label){const n=field(w,name),selector=n.closest('.ant-select')?.querySelector('.ant-select-selector');assert.ok(selector,'actual Antd select selector');selector.dispatchEvent(new MouseEvent('mousedown',{bubbles:true,cancelable:true}));await settle();const items=[...document.querySelectorAll('.ant-select-item-option-content')].filter(x=>visible(x)&&compact(x.textContent)===label);assert.equal(items.length,1,'one actual dropdown option');await click(items[0])}
function inputCallback(w,name){const id=w.vm.fieldId(name),items=w.findAllComponents(privateFields.has(name)?Antd.InputPassword:Antd.Input),c=items.find(c=>c.attributes('id')===id);assert.ok(c,'actual bound Antd input');const fn=c.vm.$.vnode.props?.['onUpdate:value'];assert.equal(typeof fn,'function','original bound input entry callback');return fn}
function saveCallback(w){const c=w.findAllComponents(Antd.Button).find(c=>compact(c.text()).replace(/\s/g,'')==='保存');assert.ok(c);const fn=c.vm.$.vnode.props?.onClick;assert.equal(typeof fn,'function');return fn}
function view(w){return{canManage:w.vm.canManageSettings(),loaded:w.vm.settingsLoaded,loading:w.vm.tabLoading,saving:w.vm.saving,dirty:w.vm.hasUnsavedChanges,draining:w.vm.settingsDraining,settingsErrorPresent:!!w.vm.settingsError,formSecretSummary:secretState(w.vm.formState),visibleGroups:[...w.element.querySelectorAll('.settings-section h2,.settings-section h3,.settings-section .console-panel__title')].map(n=>compact(n.textContent)),privateInputTypes:secretFields.map(name=>{const n=document.getElementById(w.vm.fieldId(name));return{field:name,present:!!n,type:n?.type||null,valueLength:n?.value?.length||0}})}}
async function open(actor=A){
 if(actor)user.setStoredCurrentUser(actor);else user.clearStoredCurrentUser()
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/adapter/system-settings',component:Page}]});await router.push('/adapter/system-settings');await router.isReady()
 const Shell={render:()=>h(Antd.ConfigProvider,{locale:zhCN},{default:()=>h(RouterView)})},app=mount(Shell,{attachTo:document.body,global:{plugins:[Antd,router],config:{errorHandler:e=>runtime.push(safeText(e?.stack||e))}}});roots.push({app,router});await settle()
 const w=app.findComponent(Page);assert.ok(w.exists(),'actual SystemSettings and own client permission gates are mounted, not a stub')
 if(actor?.role==='admin')await until(()=>w.vm.settingsLoaded&&w.vm.paymentMethodsLoaded&&!w.vm.tabLoading,'owned settings/payment200 loaded')
 check(app.findComponent(Antd.ConfigProvider).props('locale')?.locale==='zh-cn','actual zhCN ConfigProvider')
 return w
}
function writeLedger(){if(process.env.SYSTEM_SETTINGS_LEDGER)fs.writeFileSync(process.env.SYSTEM_SETTINGS_LEDGER,JSON.stringify({schema:'SYSTEM_SETTINGS_NATIVE_LEDGER_V1',fixtureSHA256,sourceBefore,sourceAfter:hashes(),cases,nativeDOM:true,physicalBrowser:false,actualPageChildrenAPIRequestAxiosMemory:true,noRawSecretsFormOrPayloadRecorded:true,backendAuthorizationAndRealConfigProviderNotQualified:true},null,2)+'\n')}
function register(name,fn){test(name,{timeout:20000},async()=>{cur={name,checks:[],observations:[],passed:false};cases.push(cur);try{await fn();check(realIO.length===0&&runtime.length===0&&consoleErrors.length===0,'zero real IO/runtime/console errors');const failed=cur.checks.filter(c=>!c.passed);assert.equal(failed.length,0,failed.map(c=>c.label).join('; '));cur.passed=true}catch(error){cur.error={name:error.name,message:safeText(error.message)};throw error}finally{writeLedger()}})}
afterEach(async()=>{if(!cur)return;try{if(restoreRemove){restoreRemove();restoreRemove=null}for(const{app,router}of roots.splice(0)){app.unmount();router.options.history.destroy()}await settle();Antd.message.destroy();Antd.Modal.destroyAll();user.clearStoredCurrentUser();window.localStorage.clear();document.body.replaceChildren();await settle();cur.io={calls,privateGETs:calls.filter(c=>c.method==='get').length,PUTs:calls.filter(c=>c.method==='put').length,realIO:[...realIO],runtime:[...runtime],consoleErrors:[...consoleErrors],consoleWarnings:[...consoleWarnings]};cur.savedNoticeCount=savedNotices;cur.cleanup={bodyEmpty:document.body.childElementCount===0,sourceStable:JSON.stringify(hashes())===JSON.stringify(sourceBefore)};assert.deepEqual(realIO,[]);assert.deepEqual(runtime,[]);assert.deepEqual(consoleErrors,[]);assert.ok(Object.values(cur.cleanup).every(Boolean))}catch(error){cur.passed=false;cur.hookError={name:error.name,message:safeText(error.message)};throw error}finally{cur.io=cur.io?{...cur.io,calls:[...calls]}:undefined;writeLedger();calls.length=realIO.length=runtime.length=consoleErrors.length=consoleWarnings.length=0;expectedPayload=null;metricsSerial=savedNotices=0;cur=null}})
after(()=>{axios.defaults.adapter=originalAdapter;console.error=originalConsoleError;console.warn=originalConsoleWarn;Antd.message.success=originalSuccess;writeLedger()})

register('SC01 six settings groups, native remote-local-remote hides only three controls while retaining values; one normalized full owned save preserves raw-admin/empty-clear convention',async()=>{
 const w=await open(),a=detail('A'),remote=['captchaApiBaseUrl','captchaApiKey','captchaType']
 check(w.element.querySelectorAll('.settings-section').length===8&&groups.every(t=>w.element.textContent.includes(t)),'seven exact current configuration groups remain visible and distinct')
 check(w.vm.formState.captchaProvider==='remote'&&remote.every(n=>!!document.getElementById(w.vm.fieldId(n))),'remote captcha begins with three actual controls')
 const before=calls.length;await choose(w,'captchaProvider','站内图片验证码')
 check(w.vm.formState.captchaProvider==='local'&&remote.every(n=>!document.getElementById(w.vm.fieldId(n))),'actual local option hides exactly the three remote captcha controls')
 check(remote.every(n=>w.vm.formState[n]===a[n])&&calls.length===before,'hidden controls retain original values in memory and never trigger provider/GET/PUT')
 await choose(w,'captchaProvider','远程 TAC 验证码')
 check(w.vm.formState.captchaProvider==='remote'&&remote.every(n=>!!document.getElementById(w.vm.fieldId(n)))&&field(w,'captchaApiBaseUrl').value===a.captchaApiBaseUrl&&field(w,'captchaApiKey').value===a.captchaApiKey&&w.vm.formState.captchaType===a.captchaType,'return to remote restores actual controls and exact prior values without erasure')
 type(field(w,'siteName'),'  SYNTHETIC_EDITED_SITE  ');type(field(w,'siteBaseUrl'),' https://synthetic-edited.invalid/// ');type(field(w,'uapiApiKey'),'');type(field(w,'smsbaoSecret'),'__CLEAR__');await settle()
 expectedPayload={...a,siteName:'SYNTHETIC_EDITED_SITE',epayNotifyUrl:'https://synthetic-edited.invalid/adapter-service/payment/notify',epayReturnUrl:'https://synthetic-edited.invalid/adapter/recharge/result',uapiApiKey:'',smsbaoSecret:'__CLEAR__'}
 check(w.vm.hasUnsavedChanges&&calls.filter(c=>c.method==='put').length===0,'actual edits are unsaved until explicit save')
 const reads=calls.filter(c=>c.method==='get').length;await click(button(w,'保存'));await until(()=>!w.vm.saving&&calls.some(c=>c.method==='put'),'current full save200 completes')
 const sent=calls.filter(c=>c.method==='put')
 check(sent.length===1&&sent[0].actor===A.id&&sent[0].fullNormalizedPayloadMatches&&sent[0].payloadFieldCount===35,'exact normalized full35-field payload is sent once for captured current admin, compared only in memory')
 check(sent[0].emptyClearPreserved&&sent[0].literalClearPreserved&&sent[0].secretSummary.wechatApiBearerToken.length===a.wechatApiBearerToken.length,'empty clear, literal marker and legitimate raw-admin key submission preserve current convention')
 check(calls.filter(c=>c.method==='get').length===reads&&!w.vm.hasUnsavedChanges&&savedNotices===1,'successful save marks only its config snapshot saved, with no readback/provider call')
 cur.observations.push({label:'after single full save',state:view(w),payloadComparisonBooleansOnly:{normalized:sent[0].fullNormalizedPayloadMatches,empty:sent[0].emptyClearPreserved,literal:sent[0].literalClearPreserved}})
})

register('SC02 native password eye masks and reveals intentionally; actor/role/logout synchronously scrub private pixels and retire old bound input/save entries',async()=>{
 const w=await open();let n=field(w,'wechatApiBearerToken')
 check(n.type==='password'&&n.value===detail('A').wechatApiBearerToken,'initial actual password input is masked but holds current administrator raw value in memory')
 const eye=()=>n.closest('.ant-input-password')?.querySelector('.ant-input-password-icon')
 await click(eye());check(n.type==='text'&&n.value.length>0,'actual native eye explicitly reveals current input')
 await click(eye());check(n.type==='password','actual native eye hides current input again')
 await click(eye())
 const oldA=n,oldAInput=inputCallback(w,'siteName'),oldASave=saveCallback(w);user.setStoredCurrentUser(B)
 check(oldA.value.length===0&&oldA.type==='password'&&!visible(oldA)&&secretFields.every(k=>w.vm.formState[k]===''),'actor switch synchronously clears exposed old input pixels, masks/hides old form and clears secret state before tick')
 oldAInput('SYNTHETIC_RETIRED_A');oldASave()
 check(calls.filter(c=>c.method==='put').length===0&&w.vm.formState.siteName!=='SYNTHETIC_RETIRED_A','captured A input/save entries cannot edit or write B')
 await until(()=>w.vm.settingsLoaded&&!w.vm.settingsDraining&&w.vm.formState.siteName==='SYNTHETIC_SITE_B','new B form loads')
 n=field(w,'wechatApiBearerToken');check(n.type==='password'&&n.value===detail('B').wechatApiBearerToken,'fresh B form is independently masked and populated only for B')
 await click(eye());const oldRole=n,roleInput=inputCallback(w,'siteName'),roleSave=saveCallback(w);user.setStoredCurrentUser({...B,role:'user'})
 check(oldRole.value.length===0&&oldRole.type==='password'&&!visible(oldRole)&&secretFields.every(k=>w.vm.formState[k]===''),'role downgrade synchronously scrubs private pixels and values')
 const afterRoleReads=calls.length;roleInput('SYNTHETIC_RETIRED_ROLE');roleSave();await settle()
 check(calls.length===afterRoleReads&&calls.filter(c=>c.method==='put').length===0&&!w.vm.canManageSettings(),'retired role entries make no private IO and cannot restore authority')
 user.setStoredCurrentUser(B);await until(()=>w.vm.settingsLoaded&&!w.vm.settingsDraining,'B admin explicitly restored and loaded')
 n=field(w,'wechatApiBearerToken');await click(eye());const oldLogout=n,logoutInput=inputCallback(w,'siteName'),logoutSave=saveCallback(w);user.clearStoredCurrentUser()
 check(oldLogout.value.length===0&&oldLogout.type==='password'&&!visible(oldLogout)&&secretFields.every(k=>w.vm.formState[k]===''),'logout synchronously scrubs exposed private DOM and state before tick')
 const afterLogoutReads=calls.length;logoutInput('SYNTHETIC_RETIRED_LOGOUT');logoutSave();await settle()
 check(calls.length===afterLogoutReads&&calls.filter(c=>c.method==='put').length===0&&!w.vm.canManageSettings()&&!user.getStoredCurrentUser(),'old logout entries cannot read/write or regain admin authority')
 check(![...secrets].some(v=>document.body.textContent.includes(v)),'raw synthetic secrets are not emitted into page text or diagnostics')
 cur.observations.push({label:'after actor-role-logout sequence',state:view(w),allOldInputsScrubbed:[oldA,oldRole,oldLogout].every(x=>x.value.length===0&&x.type==='password')})
})

register('SC03 actual nonadmin page and diagnostics child keep existing client denial with zero private settings/payment/metrics GET or PUT',async()=>{
 const w=await open(ordinary)
 check(!w.vm.canManageSettings()&&!w.vm.settingsLoaded&&w.element.textContent.includes('当前身份尚未确认或无系统设置管理权限'),'real nonadmin page has visible existing denial and no loaded private config')
 const refresh=button(w,'刷新'),save=button(w,'保存');check(refresh.disabled&&save.disabled,'actual toolbar refresh/save are disabled for nonadmin')
 refresh.click();save.click();await settle();w.vm.handleLoad();w.vm.handleSave();await settle()
 check(!w.element.querySelector('[data-action="refresh-system-metrics"]')&&w.findComponent(MetricsPanel).exists(),'real diagnostics child is mounted but its private panel is hidden for nonadmin');await w.findComponent(MetricsPanel).vm.handleRefresh();await settle()
 check(calls.length===0,'real page entry guards and real child diagnostic guard issue zero private GET/PUT even for explicit denied attempts')
 check(secretFields.every(k=>w.vm.formState[k]==='')&&calls.filter(c=>c.method==='put').length===0,'nonadmin never receives or writes private key values')
 cur.observations.push({label:'nonadmin scope, not server authorization proof',state:view(w),privateWireCalls:calls.length})
})

register('SC04 independently refreshed metrics are readonly and never mark dirty settings as saved or invoke settings/payment reload or PUT',async()=>{
 const w=await open(),panel=w.findComponent(MetricsPanel);assert.ok(panel.exists(),'actual diagnostics child mounted')
 await until(()=>!!panel.vm.metrics&&!panel.vm.loading,'initial actual metrics DTO accepted')
 type(field(w,'siteName'),'SYNTHETIC_UNSAVED_METRICS_INDEPENDENT');await settle()
 const before={settings:calls.filter(c=>c.url==='/adapter-service/system-settings').length,payment:calls.filter(c=>c.url==='/adapter-service/payment-methods').length,metrics:calls.filter(c=>c.url==='/adapter-service/system-metrics').length,total:panel.vm.metrics.http.total,notice:savedNotices},instance=panel.vm.$
 check(w.vm.hasUnsavedChanges&&before.metrics===1,'real unsaved configuration draft and separately loaded initial metrics are present')
 await click(w.element.querySelector('[data-action="refresh-system-metrics"]'));await until(()=>!!panel.vm.metrics&&!panel.vm.loading&&panel.vm.metrics.http.total===before.total+1,'explicit metrics refresh200 completes')
 check(calls.filter(c=>c.url==='/adapter-service/system-metrics').length===before.metrics+1&&calls.filter(c=>c.url==='/adapter-service/system-settings').length===before.settings&&calls.filter(c=>c.url==='/adapter-service/payment-methods').length===before.payment&&calls.filter(c=>c.method==='put').length===0,'one independent metrics GET never saves/reloads configuration or payment rows')
 check(w.vm.formState.siteName==='SYNTHETIC_UNSAVED_METRICS_INDEPENDENT'&&w.vm.hasUnsavedChanges&&savedNotices===before.notice&&!w.vm.saving&&w.findComponent(MetricsPanel).vm.$===instance,'metrics update preserves dirty draft/child instance and never claims configuration saved')
 cur.observations.push({label:'independent readonly metrics',state:view(w),httpCountBefore:before.total,httpCountAfter:panel.vm.metrics.http.total,configurationSavedNotice:false})
})

register('SC05 explicit logout with cache removal denied still retires exposed secrets and old input/native-save/refresh entries despite unchanged cached admin identity',async()=>{
 const w=await open(),n=field(w,'wechatApiBearerToken'),eye=n.closest('.ant-input-password')?.querySelector('.ant-input-password-icon');await click(eye)
 const oldInput=inputCallback(w,'siteName'),oldSave=button(w,'保存'),refreshComponent=w.findAllComponents(Antd.Button).find(c=>compact(c.text()).replace(/\s/g,'')==='刷新');assert.ok(refreshComponent)
 const oldRefresh=refreshComponent.vm.$.vnode.props?.onClick;assert.equal(typeof oldRefresh,'function','real captured refresh entry')
 const storage=window.localStorage,proto=Object.getPrototypeOf(storage),remove=proto.removeItem
 proto.removeItem=function(key){if(this===storage&&key==='adapter_current_user')throw new Error('SYNTHETIC_REMOVE_REFUSED');return remove.apply(this,arguments)}
 restoreRemove=()=>{proto.removeItem=remove}
 const epoch=user.getCurrentUserLogoutEpoch(),before=calls.length,siteBefore=w.vm.formState.siteName
 user.clearStoredCurrentUser()
 check(user.getCurrentUserLogoutEpoch()!==epoch&&user.getStoredCurrentUser()?.id===A.id,'real explicit logout epoch advances while denied removal genuinely retains original cached A')
 check(n.value.length===0&&n.type==='password'&&!visible(n)&&secretFields.every(k=>w.vm.formState[k]==='')&&!w.vm.canManageSettings(),'cached logout synchronously masks/hides/scrubs exposed secret state and retires permission before tick')
 oldInput('SYNTHETIC_RETIRED_CACHED_LOGOUT_EDIT')
 check(w.vm.formState.siteName!=='SYNTHETIC_RETIRED_CACHED_LOGOUT_EDIT','captured old input entry cannot edit after explicit cached logout')
 oldSave.click();oldRefresh();await settle()
 check(calls.length===before&&calls.filter(c=>c.method==='put').length===0,'old native save and captured refresh before tick cannot dispatch private GET/PUT after cached logout')
 oldRefresh();oldSave.click();await settle()
 check(calls.length===before&&calls.filter(c=>c.method==='put').length===0&&!w.vm.canManageSettings(),'after ticks old private entries stay irreversibly retired, not restored by retained cache')
 cur.observations.push({label:'cached logout, no raw secret or full form recorded',logoutEpochAdvanced:user.getCurrentUserLogoutEpoch()!==epoch,cachedAdminStillPresent:user.getStoredCurrentUser()?.id===A.id,oldInputEmpty:n.value.length===0,oldInputMasked:n.type==='password',oldInputVisible:visible(n),permission:w.vm.canManageSettings(),privateCallsBefore:before,privateCallsAfter:calls.length,siteChangedFromPrelogout:w.vm.formState.siteName!==siteBefore,state:view(w)})
})
