const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const sessionStorage = window.sessionStorage
const { effectScope, ref, reactive, watch, nextTick } = require('vue')
const { mount, flushPromises } = require('@vue/test-utils')
const { useTableColumnPreferences } = require('../../src/composables/useTableColumnPreferences.mjs')
const { useScopedSessionPreferences } = require('../../src/composables/useScopedSessionPreferences.mjs')
const Component = require('../../src/components/TableColumnSettings.vue')
let scopes=[],wrappers=[]
afterEach(async()=>{ wrappers.forEach(w=>w.unmount());wrappers=[];scopes.forEach(s=>s.stop());scopes=[];localStorage.clear();sessionStorage.clear();await nextTick();document.body.replaceChildren() })
const setup = fn => {const s=effectScope();scopes.push(s);return s.run(fn)}
const columns = () => [{key:'id',title:'标识',width:80},{key:'name',title:'名称',width:120},{key:'detail',title:'详情',width:240},{title:'操作',width:50}]
test('shared columns default all source order, keep required/unkeyed and never mutate source',()=>{
 setup(()=>{
  const input=ref(columns()),scope=ref('708:admin')
  const original=input.value.slice()
  const p=useTableColumnPreferences({pageKey:'probe',columns:input,scope,requiredKeys:['id']})
  assert.deepEqual(p.visibleColumns.value.map(c=>c.title),['标识','名称','详情','操作'])
  p.setSelectedKeys(['detail','unknown'])
  assert.deepEqual(p.selectedKeys.value,['id','detail'])
  assert.deepEqual(p.visibleColumns.value.map(c=>c.title),['标识','详情','操作'])
  assert.deepEqual(input.value,original)
  assert.equal(p.columnOptions.value[0].disabled,true)
 })
})
test('column persistence contains only validated keys, not row content or HTML',()=>{
 setup(()=>{
  const input=columns();input[1].record={token:'SYNTHETIC_DO_NOT_STORE'}
  const p=useTableColumnPreferences({pageKey:'probe',columns:input,scope:'708:admin',requiredKeys:['id']})
  p.setSelectedKeys(['name','__proto__','<script>','secret'])
  const stored=localStorage.getItem(p.scopeKey.value)
  assert.deepEqual(JSON.parse(stored),{version:1,selectedKeys:['id','name']})
  assert.doesNotMatch(stored,/SYNTHETIC|script|secret|record/)
  const again=useTableColumnPreferences({pageKey:'probe',columns:input,scope:'708:admin',requiredKeys:['id']})
  assert.deepEqual(again.selectedKeys.value,['id','name'])
 })
})
test('scope and available role columns are authoritative; logout clears visited scopes',()=>{
 setup(()=>{
  const input=ref([...columns(),{key:'username',title:'所属用户'}]),scope=ref('708:admin:all')
  const p=useTableColumnPreferences({pageKey:'probe',columns:input,scope,requiredKeys:['id']})
  p.setSelectedKeys(['username'])
  const adminKey=p.scopeKey.value
  input.value=columns();scope.value='708:user:mine'
  assert.equal(p.columnOptions.value.some(c=>c.key==='username'),false)
  p.setSelectedKeys(['detail']);const userKey=p.scopeKey.value
  scope.value=''
  assert.equal(localStorage.getItem(adminKey),null);assert.equal(localStorage.getItem(userKey),null)
  scope.value='708:admin:all';input.value=[...columns(),{key:'username',title:'所属用户'}]
  assert.deepEqual(p.selectedKeys.value,['id','name','detail','username'])
 })
})
test('malformed/blocked storage falls back safely; reset restores all and live widths',()=>{
 setup(()=>{
  const input=ref(columns()),scope='708:admin'
  const key='tikuhelper:columns:v1:probe:'+encodeURIComponent(scope)
  localStorage.setItem(key,'BROKEN_JSON')
  const p=useTableColumnPreferences({pageKey:'probe',columns:input,scope,requiredKeys:['id'],priorityKeys:['name']})
  assert.equal(p.visibleColumns.value.length,4)
  p.showPriorityColumns();assert.deepEqual(p.selectedKeys.value,['id','name'])
  input.value=[...columns().map(c=>c.key==='name'?{...c,width:300}:c)]
  assert.equal(p.visibleColumns.value[1].width,300)
  p.resetColumns();assert.equal(p.visibleColumns.value.length,4);assert.equal(localStorage.getItem(key),null)
  const bad=useTableColumnPreferences({pageKey:'blocked',columns:input,scope,storage:{getItem(){throw Error('blocked')},setItem(){throw Error('quota')},removeItem(){throw Error('blocked')}}})
  bad.setSelectedKeys([]);assert.equal(bad.visibleColumns.value.length,2)
 })
})
test('session restore is identity isolated and owner clearing cannot overwrite prior preference',()=>{
 setup(()=>{
  const scope=ref('708:admin'),state=reactive({role:undefined,status:undefined,pageSize:10})
  watch(scope,()=>{state.role=undefined;state.status=undefined;state.pageSize=10},{flush:'sync'})
  const p=useScopedSessionPreferences({pageKey:'users',scope,fields:{
   role:{get:()=>state.role,set:v=>{state.role=v},validate:v=>['admin','user'].includes(v)},
   status:{get:()=>state.status,set:v=>{state.status=v},validate:v=>['enabled','disabled'].includes(v)},
   pageSize:{get:()=>state.pageSize,set:v=>{state.pageSize=v},validate:v=>[10,20,50].includes(v)},
  }})
  state.role='user';state.status='disabled';state.pageSize=50
  const adminKey=p.scopeKey.value
  scope.value='709:admin';assert.equal(state.role,undefined);assert.equal(state.pageSize,10)
  scope.value='708:admin';assert.equal(state.role,'user');assert.equal(state.pageSize,50)
  scope.value='';assert.equal(state.role,undefined);assert.equal(sessionStorage.getItem(adminKey),null)
 })
})
test('session whitelist rejects credential fields and malformed enum/page-size values',()=>{
 setup(()=>{
  const scope='708:admin',key='tikuhelper:session:v1:users:'+encodeURIComponent(scope)
  sessionStorage.setItem(key,JSON.stringify({version:1,values:{role:'root',pageSize:999,keyword:'SYNTHETIC_PRIVATE'}}))
  const state=reactive({role:'user',pageSize:10,keyword:''})
  useScopedSessionPreferences({pageKey:'users',scope,fields:{
   role:{get:()=>state.role,set:v=>{state.role=v},validate:v=>['admin','user'].includes(v)},
   pageSize:{get:()=>state.pageSize,set:v=>{state.pageSize=v},validate:v=>[10,20].includes(v)},
   keyword:{get:()=>state.keyword,set:v=>{state.keyword=v},validate:()=>true},
  }})
  assert.equal(state.role,'user');assert.equal(state.pageSize,10);assert.equal(state.keyword,'')
  state.keyword='SYNTHETIC_NEVER_PERSIST';state.pageSize=20
  assert.doesNotMatch(sessionStorage.getItem(key),/SYNTHETIC|keyword/)
 })
})
test('actual settings popover uses native disabled required checkbox and retires stale context events',async()=>{
 const w=mount(Component,{attachTo:document.body,props:{modelValue:['id','name'],options:[{key:'id',label:'标识',disabled:true},{key:'name',label:'名称',disabled:false}],contextKey:'708:admin',priorityKeys:['id']}})
 wrappers.push(w)
 await w.find('button[aria-label="列设置"]').trigger('click')
 await flushPromises();await new Promise(r=>setTimeout(r,35));await nextTick()
 const checkbox=document.querySelector('input[type="checkbox"][value="id"]')
 assert.ok(checkbox);assert.equal(checkbox.disabled,true)
 w.vm.update(['id']);assert.deepEqual(w.emitted('update:modelValue').at(-1),[['id']])
 const count=w.emitted('update:modelValue').length
 await w.setProps({contextKey:'709:user'})
 w.vm.update(['name']);assert.equal(w.emitted('update:modelValue').length,count)
 assert.equal(w.vm.open,false)
})
