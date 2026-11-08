const {test,afterEach}=require('node:test'),assert=require('node:assert/strict'),path=require('node:path'),Module=require('node:module')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(path.join(web,'tests/helpers/vue-dom.cjs'))
const {mount,flushPromises}=require('@vue/test-utils'),{nextTick}=require('vue'),Antd=require('ant-design-vue')
const Alert=require(path.join(web,'src/components/RequestFailureAlert.vue'))
const {buildResponsePayload}=require(path.join(web,'src/utils/requestHelpers.mjs'))
const wrappers=[];const failure=n=>buildResponsePayload({status:503,data:{},headers:{'x-request-id':String(n).padStart(32,'0')}})
async function settle(){await nextTick();await flushPromises();await nextTick()}
async function fixture(props){const w=mount(Alert,{attachTo:document.body,props,global:{plugins:[Antd]}});wrappers.push(w);await settle();return w}
afterEach(async()=>{for(const w of wrappers.splice(0))w.unmount();await settle();document.body.replaceChildren();Antd.message.destroy()})
test('close01 existing alert remains nonclosable by default',async()=>{
 const w=await fixture({failure:failure(1),contextKey:1})
 assert.ok(w.find('[data-request-diagnostic]').exists())
 assert.equal(w.find('.ant-alert-close-icon').exists(),false)
 assert.equal(w.emitted('close'),undefined)
})
test('close02 optional native close emits only its current original source and context',async()=>{
 const source=failure(2),w=await fixture({failure:source,contextKey:'current',closable:true,isCurrent:(f,c)=>f===source&&c==='current'})
 const button=w.find('.ant-alert-close-icon');assert.ok(button.exists(),'missing optional owned-close interface; native boundary not exercised before implementation')
 button.element.click();await settle()
 const events=w.emitted('close')||[];assert.equal(events.length,1);assert.equal(events[0][0],source);assert.equal(events[0][1],'current')
})
test('close03 retired owner and detached old native entry cannot dismiss a newer failure',async()=>{
 let active=false,wanted=failure(3),context='old'
 const w=await fixture({failure:wanted,contextKey:context,closable:true,isCurrent:(f,c)=>active&&f===wanted&&c===context})
 const old=w.find('.ant-alert-close-icon');assert.ok(old.exists(),'missing optional close prevents guard qualification before implementation')
 old.element.click();await settle();assert.equal((w.emitted('close')||[]).length,0,'retired owner cannot emit dismiss')
 wanted=failure(4);context='new';active=true;await w.setProps({failure:wanted,contextKey:context});await settle()
 const current=w.find('.ant-alert-close-icon');assert.ok(current.exists());old.element.click();await settle()
 assert.equal((w.emitted('close')||[]).length,0,'detached old entry cannot close current')
 assert.equal(w.get('[data-request-diagnostic]').text(),String(4).padStart(32,'0'))
 current.element.click();await settle();const events=w.emitted('close')||[];assert.equal(events.length,1);assert.equal(events[0][0],wanted);assert.equal(events[0][1],'new')
})
