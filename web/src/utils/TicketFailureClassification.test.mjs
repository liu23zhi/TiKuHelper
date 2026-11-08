import {test,after} from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import {fileURLToPath,pathToFileURL} from 'node:url'
import crypto from 'node:crypto'
const web=process.env.TIKU_WEB_SOURCE||fileURLToPath(new URL('../../',import.meta.url))
const view=await import(pathToFileURL(path.join(web,'src/utils/requestFailureView.mjs')).href)
const transport=await import(pathToFileURL(path.join(web,'src/utils/requestHelpers.mjs')).href)
const ticket=await import(pathToFileURL(path.join(web,'src/utils/ticketHelpers.mjs')).href)
const files=['src/utils/requestFailureView.mjs','src/utils/requestHelpers.mjs','src/utils/ticketHelpers.mjs']
const H=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),hashes=()=>Object.fromEntries(files.map(p=>[path.join(web,p),H(path.join(web,p))])),before=hashes()
const records=[];let calls=0
function feature(){assert.equal(typeof view.inheritRequestFailureView,'function','feature missing: inheritRequestFailureView export');return(...args)=>{calls+=1;return view.inheritRequestFailureView(...args)}}
function raw(status,data={},id){return transport.buildResponsePayload({status,data,headers:id?{'x-request-id':id}:{}})}
function pair(inherit,target,source){transport.inheritTransportRequestId(target,source);inherit(target,source)}
function shape(error){return{names:Object.getOwnPropertyNames(error),keys:Object.keys(error),name:error.name,message:error.message,statusType:typeof error.status}}
function projection(value){const v=view.buildRequestFailureView(value);return{kind:v.kind,title:v.title,description:v.description,allowRetry:v.allowRetry}}
function group(name,run){test(name,()=>{const start=calls;let passed=false;try{run();passed=true}finally{records.push({name,passed,actualInheritanceCalls:calls-start,boundariesInvoked:calls>start})}})}

group('P01 known HTTP classes retain public Error shape with independently inherited validated ID',()=>{
  const inherit=feature()
  for(const status of [403,429,500,401]){
    const source=raw(status,{message:'SYNTHETIC_PRIVATE_SERVER_MESSAGE'},status.toString(16).padStart(32,'0')),target=new Error('SYNTHETIC_PUBLIC_ERROR'),old=shape(target)
    pair(inherit,target,source)
    assert.deepEqual(projection(target),projection(source));assert.equal(transport.getTransportRequestId(target),transport.getTransportRequestId(source));assert.deepEqual(shape(target),old);assert.equal(target.status,undefined)
    const v=view.buildRequestFailureView(target);assert.ok(Object.isFrozen(v));assert.ok(Object.values(v).every(x=>x===undefined||typeof x==='string'||typeof x==='boolean'));assert.ok(!JSON.stringify(v).includes('SYNTHETIC_PRIVATE_SERVER_MESSAGE'))
  }
  const good=raw(200,{syntheticOnly:true});assert.equal(ticket.requireTicketResponse(good),good)
})

group('P02 business and request-semantic known classes work without a server diagnostic channel',()=>{
  const inherit=feature()
  const sources=[...['INSUFFICIENT_BALANCE','NO_ANSWER','INVALID_INPUT'].map(code=>raw(400,{error:{code},message:'SYNTHETIC_PRIVATE'})),{code:'UNKNOWN_OUTCOME'},{code:'ERR_NETWORK'},{code:'ECONNABORTED'}]
  for(const source of sources){const target=new Error('PUBLIC');inherit(target,source);assert.deepEqual(projection(target),projection(source));assert.equal(transport.getTransportRequestId(target),undefined);assert.equal(view.buildRequestFailureView(target).diagnosticId,undefined)}
  const target=new Error('PUBLIC');inherit(target,{code:'UNKNOWN_OUTCOME'});assert.equal(view.buildRequestFailureView(target).kind,'unknown-outcome');assert.equal(view.buildRequestFailureView(target).allowRetry,false)
})

group('P03 frozen targets, self-copy and explicit reuse clear stale class when only validated ID changes',()=>{
  const inherit=feature(),a=raw(403,{},'a'.repeat(32)),b=raw(500,{},'b'.repeat(32)),target=Object.freeze(new Error('PUBLIC')),old=shape(target)
  pair(inherit,target,a);assert.equal(view.buildRequestFailureView(target).kind,'forbidden');inherit(target,target);assert.equal(view.buildRequestFailureView(target).kind,'forbidden');assert.deepEqual(shape(target),old)
  transport.inheritTransportRequestId(target,b);assert.equal(view.buildRequestFailureView(target,'FIXED').kind,'unknown');assert.equal(view.buildRequestFailureView(target).diagnosticId,'b'.repeat(32))
  transport.inheritTransportRequestId(target,a);assert.equal(view.buildRequestFailureView(target,'FIXED').kind,'unknown','mismatched snapshot was cleared, not merely hidden')
  pair(inherit,target,b);assert.equal(view.buildRequestFailureView(target).kind,'upstream')
  pair(inherit,target,raw(499,{message:'PRIVATE'},'b'.repeat(32)));assert.equal(view.buildRequestFailureView(target,'TARGET_FIXED').kind,'unknown');assert.equal(view.buildRequestFailureView(target,'TARGET_FIXED').description,'TARGET_FIXED');assert.deepEqual(shape(target),old)
})

group('P04 primitive and hostile getters are safe and do not retain or reread original object getters',()=>{
  const inherit=feature()
  for(const target of [undefined,null,0,'text',true])assert.doesNotThrow(()=>inherit(target,{status:403}))
  const target=new Error('PUBLIC');inherit(target,{status:403});let reads=0
  const hostile={};for(const key of ['response','data','status','code','name','message'])Object.defineProperty(hostile,key,{get(){reads+=1;throw new Error('SYNTHETIC_GETTER')}})
  assert.doesNotThrow(()=>inherit(target,hostile));assert.equal(view.buildRequestFailureView(target,'FIXED').kind,'unknown');const captured=reads;view.buildRequestFailureView(target,'FIXED');assert.equal(reads,captured)
  let statusReads=0,payloadReads=0;const known={get status(){statusReads+=1;return 403},get payload(){payloadReads+=1;throw new Error('DO_NOT_READ')}}
  inherit(target,known);const atCapture=statusReads;assert.equal(view.buildRequestFailureView(target).kind,'forbidden');assert.equal(statusReads,atCapture);assert.equal(payloadReads,0)
})

group('P05 copied classification stays immutable after source mutation until a new explicit inheritance call',()=>{
  const inherit=feature(),source=raw(403,{message:'SYNTHETIC_PRIVATE_SOURCE'},'c'.repeat(32)),target=new Error('PUBLIC')
  pair(inherit,target,source);const original=projection(target);source.status=500;source.data.error={code:'NO_ANSWER'};source.data.message='SYNTHETIC_CHANGED_PRIVATE'
  assert.deepEqual(projection(target),original);assert.ok(!JSON.stringify(view.buildRequestFailureView(target)).includes('PRIVATE'))
  inherit(target,source);assert.equal(view.buildRequestFailureView(target).kind,'no-answer');assert.equal(view.buildRequestFailureView(target).allowRetry,false)
  const second=new Error('PUBLIC_SECOND');pair(inherit,second,target);assert.deepEqual(projection(second),projection(target));assert.equal(transport.getTransportRequestId(second),'c'.repeat(32))
})

group('P06 unknown source cannot import private fallback/message and class inheritance never mutates ID storage',()=>{
  const inherit=feature(),target=new Error('PUBLIC'),source=raw(499,{message:'SYNTHETIC_PRIVATE_BODY',detail:'SYNTHETIC_PRIVATE_DETAIL'},'d'.repeat(32))
  pair(inherit,target,raw(403,{},'d'.repeat(32)));assert.equal(view.buildRequestFailureView(target).kind,'forbidden');inherit(target,source)
  const v=view.buildRequestFailureView(target,'TARGET_FIXED_SAFE');assert.equal(v.kind,'unknown');assert.equal(v.description,'TARGET_FIXED_SAFE');assert.ok(!JSON.stringify(v).includes('PRIVATE'));assert.equal(transport.getTransportRequestId(target),'d'.repeat(32));assert.equal(target.message,'PUBLIC');assert.equal(target.status,undefined)
  transport.inheritTransportRequestId(target,{});inherit(target,{status:500});assert.equal(view.buildRequestFailureView(target).kind,'upstream');assert.equal(transport.getTransportRequestId(target),undefined)
  inherit(target,undefined);assert.equal(view.buildRequestFailureView(target,'FINAL_FIXED').kind,'unknown');assert.equal(view.buildRequestFailureView(target,'FINAL_FIXED').description,'FINAL_FIXED')
})

after(()=>{const result={schema:'TICKET_CLASSIFICATION_PURE_V1',records,actualInheritanceCalls:calls,missingExport:typeof view.inheritRequestFailureView!=='function',sourceBefore:before,sourceAfter:hashes(),sourceUnchanged:JSON.stringify(before)===JSON.stringify(hashes()),scope:'Pure public production exports; missing export does not qualify any inheritance sub-boundary. No private map getter,network,DB orProvider.'};if(process.env.CLASSIFICATION_PURE_LEDGER)fs.writeFileSync(process.env.CLASSIFICATION_PURE_LEDGER,JSON.stringify(result,null,2)+'\n');console.log('CLASSIFICATION_PURE_RESULT '+JSON.stringify({tests:records.length,pass:records.filter(x=>x.passed).length,fail:records.filter(x=>!x.passed).length,actualInheritanceCalls:calls,missingExport:result.missingExport,sourceUnchanged:result.sourceUnchanged}))})
