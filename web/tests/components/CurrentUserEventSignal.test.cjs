const {test,afterEach}=require('node:test'),assert=require('node:assert/strict'),path=require('node:path'),Module=require('node:module')
const web=process.env.TIKU_WEB_SOURCE||path.resolve(__dirname,'../..')
process.env.NODE_PATH=[path.join(web,'node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
require(process.env.UI_TEST_HELPER||path.join(web,'tests/helpers/vue-dom.cjs'))
const {effectScope,watch,isReadonly,isShallow}=require('vue'),user=require(path.join(web,'src/utils/currentUser.js'))
let scopes=[],descriptor
afterEach(()=>{for(const scope of scopes)scope.stop();scopes=[];if(descriptor){Object.defineProperty(window,'localStorage',descriptor);descriptor=null}user.clearStoredCurrentUser()})
test('explicit logout signals a direct shallow display watcher even when storage is blocked and null stays null',()=>{
 descriptor=Object.getOwnPropertyDescriptor(window,'localStorage')
 Object.defineProperty(window,'localStorage',{configurable:true,get(){throw new Error('SYNTHETIC blocked storage')}})
 const scope=effectScope();scopes.push(scope);let notifications=0
 scope.run(()=>{const{currentUser}=user.useCurrentUserDisplay();assert.equal(currentUser.value,null);watch(currentUser,()=>notifications++,{flush:'sync'})})
 const epoch=user.getCurrentUserLogoutEpoch();user.clearStoredCurrentUser()
 assert.equal(user.getCurrentUserLogoutEpoch(),epoch+1)
 assert.equal(notifications,1,'authoritative clear must synchronously retire private local confirmed data')
})
test('forced object profile writes notify once and unknown storage alone does not invent logout',()=>{
 user.clearStoredCurrentUser();const scope=effectScope();scopes.push(scope);let notifications=0
 scope.run(()=>{const{currentUser}=user.useCurrentUserDisplay();watch(currentUser,()=>notifications++,{flush:'sync'})})
 const actor={id:76101,role:'user',username:'SYNTHETIC_ACTOR'}
 user.setStoredCurrentUser(actor);assert.equal(notifications,1)
 user.setStoredCurrentUser(actor);assert.equal(notifications,2,'already-new object must not receive a duplicate forced notification')
 const before=user.getCurrentUserLogoutEpoch();const lease=user.captureCurrentUserLease()
 assert.equal(lease.isRetired(),false);assert.equal(user.getCurrentUserLogoutEpoch(),before);lease.release()
})

test('display remains deeply readonly while retaining the shallow force notification contract',()=>{
 user.setStoredCurrentUser({id:76102,role:'user',nested:{nickname:'SYNTHETIC_READONLY'}})
 const scope=effectScope();scopes.push(scope);let current
 scope.run(()=>{current=user.useCurrentUserDisplay().currentUser})
 assert.equal(isReadonly(current),true);assert.equal(isShallow(current),true)
 assert.equal(isReadonly(current.value),true);assert.equal(isReadonly(current.value.nested),true)
 const copy=user.getStoredCurrentUser();copy.nested.nickname='SYNTHETIC_MUTATION'
 assert.equal(current.value.nested.nickname,'SYNTHETIC_READONLY')
})
