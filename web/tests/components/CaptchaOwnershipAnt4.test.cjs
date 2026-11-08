const {test,beforeEach,afterEach}=require('node:test'),assert=require('node:assert/strict'),path=require('node:path'),Module=require('node:module')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'))
for(const key of ['DOMParser','HTMLVideoElement','HTMLAudioElement'])globalThis[key]=window[key]
window.scrollTo=()=>{}
require.extensions['.css']=(module)=>{module.exports={}}
const Swal=require('sweetalert2'),{flushPromises}=require('@vue/test-utils'),{nextTick}=require('vue')
const file=path.join(web,'src/utils/captchaDialog.mjs'),IMAGE='data:image/png;base64,iVBORw0KGgo='
let helper,controllers=[],promises=[],sdkOptions=[]
function pending(){let resolve;const promise=new Promise(r=>resolve=r);return{resolve,promise}}
async function settle(){await nextTick();await flushPromises();await new Promise(r=>setTimeout(r,4));await nextTick()}
function local(extra={}){return {scene:'login_password',targetForm:{captchaToken:''},publicSettings:{captchaProvider:'local'},createCaptchaChallenge:async()=>({status:200,data:{challengeId:'SYNTHETIC_CHALLENGE',imageData:IMAGE}}),verifyCaptchaChallenge:async()=>({status:200,data:{captchaToken:'SYNTHETIC_VERIFIED'}}),...extra}}
function track(promise){const result={error:null,value:null};promises.push(promise.then(value=>{result.value=value},error=>{result.error=error}));return result}
function controller(){const c=new AbortController();controllers.push(c);return c}
beforeEach(()=>{delete require.cache[file];helper=require(file);delete window.initTAC})
afterEach(async()=>{for(const c of controllers)c.abort();for(const config of sdkOptions)config.btnCloseFun?.(null,{destroyWindow(){}});Swal.close();await settle();await Promise.all(promises);controllers=[];promises=[];sdkOptions=[];document.body.replaceChildren();delete window.initTAC})
test('CAPTCHA default local flow retains real challenge, explicit code and verify before writing token',async()=>{
 const opts=local(),calls=[];opts.verifyCaptchaChallenge=async payload=>{calls.push(payload);return{status:200,data:{captchaToken:'SYNTHETIC_VERIFIED'}}}
 const p=helper.ensureAuthCaptchaToken(opts);track(p);await settle();assert.ok(Swal.getPopup())
 Swal.getPopup().querySelector('#captcha-code-input').value='1234';Swal.clickConfirm();await p
 assert.equal(calls.length,1);assert.equal(calls[0].challengeId,'SYNTHETIC_CHALLENGE');assert.equal(calls[0].captchaCode,'1234');assert.equal(opts.targetForm.captchaToken,'SYNTHETIC_VERIFIED')
})
test('CAPTCHA aborted challenge cannot open a popup or verify after the issued request completes',async()=>{
 const c=controller(),q=pending();let verify=0
 const opts=local({signal:c.signal,createCaptchaChallenge:()=>q.promise,verifyCaptchaChallenge:async()=>{verify++;return{status:200}}})
 const result=track(helper.ensureAuthCaptchaToken(opts));c.abort();q.resolve({status:200,data:{challengeId:'SYNTHETIC_OLD',imageData:IMAGE}});await settle()
 assert.equal(Swal.getPopup(),null,'retired challenge must not create a dialog');assert.equal(verify,0);assert.equal(result.error?.name,'AbortError');assert.equal(opts.targetForm.captchaToken,'')
})
test('CAPTCHA aborted verification cannot write a late token into a new form',async()=>{
 const c=controller(),q=pending();let calls=0
 const opts=local({signal:c.signal,verifyCaptchaChallenge:()=>{calls++;return q.promise}})
 const result=track(helper.ensureAuthCaptchaToken(opts));await settle();Swal.getPopup().querySelector('#captcha-code-input').value='1234';Swal.clickConfirm();await settle();assert.equal(calls,1)
 c.abort();q.resolve({status:200,data:{captchaToken:'SYNTHETIC_OLD_TOKEN'}});await settle()
 assert.equal(opts.targetForm.captchaToken,'');assert.equal(result.error?.name,'AbortError')
})
test('CAPTCHA old refresh cannot change or destroy a later owned popup',async()=>{
 const c=controller(),q=pending();track(helper.openCaptchaDialog({imageData:IMAGE,refreshCaptchaChallenge:()=>q.promise,signal:c.signal}));await settle()
 Swal.getPopup().querySelector('#captcha-refresh-trigger').click();await settle()
 track(helper.openCaptchaDialog({imageData:IMAGE,refreshCaptchaChallenge:async()=>IMAGE}));await settle()
 const current=Swal.getPopup(),hint=current.querySelector('#captcha-dialog-hint');hint.textContent='SYNTHETIC_B_HINT'
 c.abort();q.resolve(IMAGE);await settle()
 assert.strictEqual(Swal.getPopup(),current);assert.equal(hint.textContent,'SYNTHETIC_B_HINT')
 Swal.close()
})
test('CAPTCHA default remote flow verifies the SDK result rather than trusting it as the server token',async()=>{
 let init=0,verify=0,destroy=0
 window.initTAC=async(path,options)=>{sdkOptions.push(options);return{init(){init++;options.validSuccess({captchaId:'SYNTHETIC_ID'},null,this)},destroyWindow(){destroy++}}}
 const opts=local({publicSettings:{captchaProvider:'remote',captchaApiBaseUrl:'https://synthetic.invalid',captchaType:'RANDOM'},verifyRemoteCaptcha:async payload=>{verify++;assert.deepEqual(payload,{scene:'login_password',captchaId:'SYNTHETIC_ID'});return{status:200,data:{captchaToken:'SYNTHETIC_SERVER_TOKEN'}}}})
 const p=helper.ensureAuthCaptchaToken(opts);track(p);await p;assert.equal(init,1);assert.equal(verify,1);assert.equal(destroy,1);assert.equal(opts.targetForm.captchaToken,'SYNTHETIC_SERVER_TOKEN')
})
test('CAPTCHA aborted SDK loading cannot init the widget, verify, or fall back to a new local call',async()=>{
 const c=controller();let init=0,verify=0,localCalls=0
 const opts=local({signal:c.signal,publicSettings:{captchaProvider:'remote',captchaApiBaseUrl:'https://synthetic.invalid',captchaType:'RANDOM'},createCaptchaChallenge:async()=>{localCalls++;return{status:500}},verifyRemoteCaptcha:async()=>{verify++;return{status:200,data:{captchaToken:'SYNTHETIC_BAD'}}}})
 const result=track(helper.ensureAuthCaptchaToken(opts));await settle();const script=[...document.head.querySelectorAll('script')].find(s=>s.src.includes('synthetic.invalid'));assert.ok(script)
 c.abort();window.initTAC=async(path,options)=>{sdkOptions.push(options);return{init(){init++;options.validSuccess({captchaId:'SYNTHETIC_OLD'},null,this)},destroyWindow(){}}};script.onload();await settle()
 assert.equal(init,0);assert.equal(verify,0);assert.equal(localCalls,0);assert.equal(result.error?.name,'AbortError');assert.equal(opts.targetForm.captchaToken,'')
 script.remove()
})
