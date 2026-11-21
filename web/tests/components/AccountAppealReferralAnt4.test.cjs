const {test,afterEach}=require('node:test'),assert=require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const {mount,flushPromises,config}=require('@vue/test-utils'),{nextTick}=require('vue'),{createRouter,createMemoryHistory}=require('vue-router'),Antd=require('ant-design-vue'),axios=require('axios')
for(const name of ['ConsolePageHeading','ConsolePanel','ConsoleSummaryGrid','ConsoleTableShell'])delete config.global.components[name]
const {registerConsoleUI}=require('../../src/registerUI.js')
const user=require('../../src/utils/currentUser.js'),calls=[],mounts=[]
const admin={id:771,username:'admin-fixture',role:'admin',status:'enabled'}
axios.defaults.adapter=config=>{
 const method=String(config.method||'get'),url=String(config.url),data=typeof config.data==='string'?JSON.parse(config.data):config.data;calls.push({method,url,data})
 let body,status=200
 if(url==='/api/admin/referral-settings')body={enabled:true,rate:0.125}
 else if(url==='/api/user/review-rebate/availability'&&method==='get')body={enabled:true}
 else if(url==='/api/tickets/31/account-action'&&method==='post')body={message:'账号处理完成'}
 else if(url==='/api/admin/review-rebate/programs'&&method==='post'){body={message:'活动名称不能为空'};status=400}
 else if(['/api/admin/review-rebate/programs','/api/admin/review-rebate/rules','/api/admin/review-rebate/claims'].includes(url))body={data:[]}
 else throw new Error('unexpected fixture operation '+method+' '+url)
 return Promise.resolve({status,data:body,headers:{},config,request:{memoryOnly:true}})
}
const Sider=require('../../src/components/SiderMenu/index.vue'),Referral=require('../../src/components/ReferralSettingsPanel.vue'),Review=require('../../src/pages/ReviewRebateAdmin/index.vue'),Actions=require('../../src/pages/Tickets/TicketAccountActions.vue')
async function settle(){await nextTick();await flushPromises();await nextTick();await flushPromises()}
async function page(component,props={}){user.setStoredCurrentUser(admin);const router=createRouter({history:createMemoryHistory(),routes:[{path:'/fixture',component:{template:'<div />'}}]});await router.push('/fixture');await router.isReady();const wrapper=mount(component,{props,attachTo:document.body,global:{plugins:[{install:registerConsoleUI},router]}});mounts.push({wrapper,router});await settle();return wrapper}
afterEach(async()=>{for(const {wrapper,router}of mounts.splice(0)){wrapper.unmount();router.options.history.destroy()}Antd.Modal.destroyAll();Antd.message.destroy();await settle();document.body.replaceChildren();calls.length=0;user.clearStoredCurrentUser()})
test('banned administrator sees only feedback and enabled administrator shares referral and review entries',async()=>{
 const wrapper=await page(Sider,{currentUser:{...admin,status:'banned'}})
 assert.match(wrapper.text(),/工单反馈/)
 assert.doesNotMatch(wrapper.text(),/系统管理|个人中心|我的邀请|评价返现|工单管理/)
 await wrapper.setProps({currentUser:admin});await settle()
 assert.match(wrapper.text(),/我的邀请/);assert.equal(wrapper.get('a[href="/review-rebate"]').text(),'评价返现');assert.match(wrapper.text(),/邀请码管理/)
})
test('referral panel reads percent and saves only dedicated reward settings',async()=>{
 const wrapper=await page(Referral)
 const state=wrapper.vm.$.setupState
 assert.equal(state.form.percent,12.5)
 const input=wrapper.find('input[role="spinbutton"]');await input.setValue('7.5')
 const save=wrapper.findAll('button').find(button=>button.text()==='保存邀请配置');assert.ok(save.exists());await save.trigger('click');await settle()
 assert.deepEqual(calls.find(item=>item.method==='put').data,{enabled:true,rate:0.075})
 assert.ok(calls.every(item=>item.url==='/api/admin/referral-settings'))
})
test('review program HTTP400 displays the concrete server reason and retires it on identity change',async()=>{
 const wrapper=await page(Review)
 await wrapper.vm.saveProgram();await settle()
 assert.match(wrapper.text(),/活动名称不能为空/)
 assert.equal(calls.filter(item=>item.method==='post').length,1)
 user.setStoredCurrentUser({...admin,id:772});await settle()
 assert.doesNotMatch(wrapper.text(),/活动名称不能为空/)
})
test('ticket account actions are absent for ordinary accounts and non-account tickets',async()=>{
 const wrapper=await page(Actions,{ticket:{id:31,userId:11,category:'account'}})
 assert.match(wrapper.text(),/账号处理/)
 user.setStoredCurrentUser({...admin,role:'user'});await settle();assert.doesNotMatch(wrapper.text(),/账号处理/)
 user.setStoredCurrentUser(admin);await wrapper.setProps({ticket:{id:31,userId:11,category:'refund'}});await settle();assert.doesNotMatch(wrapper.text(),/账号处理/)
 assert.equal(calls.length,0)
})
test('native account-action submit requires confirmation before one scoped request',async()=>{
 const wrapper=await page(Actions,{ticket:{id:31,userId:11,category:'account'}})
 await wrapper.find('textarea').setValue('已核验申诉人身份')
 await wrapper.find('button[type="submit"]').trigger('click');await settle()
 assert.equal(calls.length,0)
 const confirmation=document.querySelector('.ant-modal-confirm .ant-btn-primary');assert.ok(confirmation,'a real confirmation opens after native form submit')
 confirmation.click();await settle()
 assert.equal(calls.length,1);assert.equal(calls[0].url,'/api/tickets/31/account-action');assert.equal(calls[0].data.action,'unban');assert.equal(Object.hasOwn(calls[0].data,'userId'),false)
})
