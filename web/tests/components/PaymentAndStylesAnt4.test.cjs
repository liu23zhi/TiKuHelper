const {test,afterEach}=require('node:test')
const assert=require('node:assert/strict'),path=require('node:path'),fs=require('node:fs'),Module=require('node:module')
const root=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
require(process.env.UI_TEST_HELPER||path.resolve(__dirname,'../helpers/vue-dom.cjs'))
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick}=require('vue'),Antd=require('ant-design-vue')
const user=require(path.join(root,'src/utils/currentUser.js'))
let wrapper;const writes=[]
const apiPath=path.join(root,'src/api/TikuComponentApi.js');const api=new Module(apiPath);api.loaded=true;api.exports={getSystemSettings:()=>Promise.resolve({status:200,data:{}}),getPaymentMethods:()=>Promise.resolve({status:200,data:[]}),createPaymentMethod:p=>{writes.push(p);return Promise.resolve({status:200,data:{}})},updatePaymentMethod:p=>{writes.push(p);return Promise.resolve({status:200,data:{}})}};require.cache[apiPath]=api
async function settle(){await nextTick();await flushPromises();await nextTick()}
afterEach(async()=>{wrapper?.unmount();wrapper=undefined;Antd.Modal.destroyAll();Antd.message.destroy();await settle();document.body.innerHTML='';user.clearStoredCurrentUser();writes.length=0})
function load(rel){const p=path.join(root,rel);assert.ok(fs.existsSync(p),'required migrated source '+rel);return require(p)}
test('original 77 vectors plus 9 bounded metadata regressions is parsed, not interpreted as HTML',()=>{const safe=load('src/utils/safePaymentIcon.mjs');const raw=fs.readFileSync(path.join(root,'src/utils/paymentIconContract.json'));assert.equal(require('node:crypto').createHash('sha256').update(raw).digest('hex'),'508d6a5a62939ddc94734c0ad3bc2588b854577e3313f62a1912065d2c9a2e0b');const vectors=JSON.parse(raw);assert.equal(vectors.length,86);assert.equal(require('node:crypto').createHash('sha256').update(JSON.stringify(vectors.slice(0,77))).digest('hex'),'fa465844a6ef56b1c7810811ffa2e33b28e5da04dcf3d18f3baca4c3749a8b03');for(const x of vectors)assert.equal(safe.isSafePaymentIcon(x.svg),x.valid,x.id)})
test('actual Vue safe renderer reconstructs static SVG and preserves unsupported historical strings as a placeholder',()=>{const Icon=load('src/components/SafePaymentIcon.js').default;wrapper=mount(Icon,{props:{value:'<svg viewBox="0 0 24 24"><rect width="2" height="3"/></svg>'}});assert.ok(wrapper.find('svg rect').exists());wrapper.unmount();const legacy='<svg data-fixture="historical"/>';wrapper=mount(Icon,{props:{value:legacy}});assert.ok(wrapper.find('[data-payment-icon-placeholder]').exists());assert.equal(wrapper.props('value'),legacy);assert.equal(wrapper.find('svg').exists(),false)})
test('all 17 trusted icon catalog components serialize into the unchanged static contract',()=>{const {paymentIconCatalog,serializePaymentIcon}=load('src/utils/paymentIconCatalog.js');const {isSafePaymentIcon}=load('src/utils/safePaymentIcon.mjs');assert.equal(paymentIconCatalog.length,17);for(const x of paymentIconCatalog)assert.equal(isSafePaymentIcon(serializePaymentIcon(x.id)),true,x.id)})
test('existing SystemSettings fields stay intact; historical unsupported value is not rewritten; real chooser produces an accepted icon',async()=>{user.setStoredCurrentUser({id:71,username:'SYNTHETIC_PAYMENT_ADMIN',role:'admin'});const Page=load('src/pages/SystemSettings/index.vue');wrapper=mount(Page,{attachTo:document.body,global:{plugins:[Antd]}});await settle();const legacy='<svg data-fixture="historical"/>';wrapper.vm.openEditPaymentMethodModal({id:71,name:'FixturePay',iconSvg:legacy,epayType:'fixture-pay',enabled:true,sort:8,remark:'upstream remark'});await settle();assert.equal(wrapper.vm.paymentMethodFormState.iconSVG,legacy);wrapper.vm.handleSubmitPaymentMethod();await settle();assert.equal(writes.length,0,'unsupported historical SVG cannot be submitted silently');const buttons=[...document.querySelectorAll('.payment-icon-button')];assert.equal(buttons.length,17);buttons[0].click();await settle();assert.equal(wrapper.vm.paymentMethodIconCatalogId,'antd:alipay:v1');assert.equal(wrapper.vm.isSafePaymentIcon(wrapper.vm.paymentMethodFormState.iconSVG),true);assert.equal(wrapper.vm.paymentMethodFormState.remark,'upstream remark');assert.equal(wrapper.vm.paymentMethodFormState.ePayType,'fixture-pay');wrapper.vm.handleSubmitPaymentMethod();await settle();assert.equal(writes.length,1);assert.equal(writes[0].id,71);assert.equal(writes[0].remark,'upstream remark')})
test('both existing payment icon consumers avoid arbitrary v-html and captcha-only CSS is awaited before local and remote mount',()=>{for(const name of ['UserUsage','SystemSettings']){const s=fs.readFileSync(path.join(root,'src/pages',name,'index.vue'),'utf8');assert.match(s,/SafePaymentIcon/);assert.doesNotMatch(s,/v-html=/)}const c=fs.readFileSync(path.join(root,'src/utils/captchaDialog.mjs'),'utf8');assert.match(c,/import\(\/\* webpackChunkName: "captcha-geometry" \*\/ '\.\.\/styles\/captcha-geometry-tokens\.css'\)/);assert.equal((c.match(/await guard\.run\(ensureCaptchaGeometryStyles\)/g)||[]).length,2);const local=c.slice(c.indexOf('export async function openCaptchaDialog'),c.indexOf('export async function openRemoteCaptchaDialog'));const remote=c.slice(c.indexOf('export async function openRemoteCaptchaDialog'));assert.match(local,/await guard\.run\(ensureCaptchaGeometryStyles\)[\s\S]*?const resultPromise = Swal\.fire\(/);assert.match(remote,/await guard\.run\(ensureCaptchaGeometryStyles\)[\s\S]*?const \{ mount, mountId \} = createRemoteCaptchaMount\(\)/);assert.match(c,/captchaGeometryStylesPromise = null/);const main=fs.readFileSync(path.join(root,'src/main.js'),'utf8');assert.doesNotMatch(main,/captcha-geometry/);const layout=fs.readFileSync(path.join(root,'src/layout/LoginLayout.vue'),'utf8');assert.match(layout,/var\(--surface-subtle\)/)})

test('upstream prop spelling uses bounded SVG geometry with a fixed-size trusted renderer', () => {
  const Icon=load('src/components/SafePaymentIcon.vue')
  wrapper=mount(Icon,{props:{iconSvg:'<svg width="1000000" height="1000000"><path d="M0 0h1"/></svg>',fallback:'备'}})
  assert.ok(wrapper.find('svg path').exists())
  assert.equal(wrapper.element.style.width,'28px')
  assert.equal(wrapper.find('svg').element.style.width,'100%')
})

// 防漂移：paymentIconContract.json 在仓库里被声明为 `-text`（见根 .gitattributes）。
// 一旦有人把它改成 `text`（或删掉那条规则），在 core.autocrlf=true 的 Windows 上
// 检出就会被转成 CRLF，上面那条 sha256 断言随即红 —— 但它的报错只会说
// 「a67ac51c… != 508d6a5a…」，看的人完全不知道是换行被转换了。
// 这条用例把「字节里不能有 CR」单独钉出来，让失败信息直接指向根因。
//
// 为什么不能只写 assert(!raw.includes('\r\n'))：那样文件若变成空 / 单行，
// 断言会因为「压根没有换行」而白通过（假钉子）。所以先断言 lfCount > 0。
test('paymentIconContract.json is LF-only on disk (guards the -text rule in .gitattributes)',()=>{
  const file=path.join(root,'src/utils/paymentIconContract.json')
  const raw=fs.readFileSync(file)
  let crCount=0,lfCount=0
  for(const byte of raw){if(byte===13)crCount++;else if(byte===10)lfCount++}
  assert.ok(lfCount>0,'fixture must contain newlines, otherwise the CR assertion below is vacuous (lf='+lfCount+')')
  assert.equal(crCount,0,
    'paymentIconContract.json has '+crCount+' CR bytes on disk. Either its `-text` rule was '+
    'dropped from .gitattributes (so core.autocrlf=true rewrote it to CRLF on checkout), or the '+
    'file was committed with CRLF. Keep it `-text`; its sha256 is pinned by the assertion above.')
})
