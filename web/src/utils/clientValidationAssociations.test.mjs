import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
const web=process.env.TIKU_WEB_SOURCE||path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..')
const tickets=await import(pathToFileURL(path.join(web,'src/utils/ticketHelpers.mjs')).href)
const extension=await import(pathToFileURL(path.join(web,'src/utils/extensionUpdateHelpers.mjs')).href)
const ticketCases=[
 ['', '', 'subject','请输入工单标题'],[' \r\n ', 'body','subject','请输入工单标题'],
 ['🚀'.repeat(120),'body','',''],['🚀'.repeat(121),'','subject','工单标题不能超过 120 个字符'],
 ['title','','body','请输入问题描述'],['title',' \r\n\t','body','请输入问题描述'],
 ['title','😀'.repeat(10000),'',''],['title','😀'.repeat(10001),'body','问题描述不能超过 10000 个字符'],
 [null,'body','subject','请输入工单标题'],['title','a\r\nb','',''],
]
const intervalMessage='检查间隔须为至少 1 分钟且不超过系统支持范围。'
const edgeMessage='Edge 扩展 ID 须为 32 位 a 到 p 的小写字母，留空可停用。'
const chromeMessage='Chrome 扩展 ID 须为 32 位 a 到 p 的小写字母，留空可停用。'
const config=(intervalMinutes=15,edgeId='',chromeId='')=>({intervalMinutes,edgeId,chromeId})
const extensionCases=[
 [config(1),'',''],[config('15'),'',''],[config(0,'invalid','invalid'),'intervalMinutes',intervalMessage],
 [config(NaN),'intervalMinutes',intervalMessage],[config(1.5),'intervalMinutes',intervalMessage],
 [config(extension.EXTENSION_INTERVAL_MAX_MINUTES),'',''],[config(extension.EXTENSION_INTERVAL_MAX_MINUTES+1),'intervalMinutes',intervalMessage],
 [config(15,'B','q'.repeat(32)),'edgeId',edgeMessage],[config(15,'a'.repeat(32),'q'.repeat(32)),'chromeId',chromeMessage],
 [config(15,' '+ 'a'.repeat(32)+' '),'edgeId',edgeMessage],[config(15,0,false),'',''],[config(15,42),'edgeId',edgeMessage],
]
function metadata(module,name){assert.equal(typeof module[name],'function','missing field-metadata export; sub-boundaries not invoked before implementation');return module[name]}
test('V01 legacy string validators preserve normalization, rune lengths, exact reasons and priority',()=>{
 for(const [subject,body,,message] of ticketCases)assert.equal(tickets.validateTicketDraft(subject,body),message)
 for(const [value,,message] of extensionCases)assert.equal(extension.validateExtensionConfig(value),message)
 assert.throws(()=>extension.validateExtensionConfig(undefined),TypeError)
})
test('V02 Ticket metadata returns the same current reason and corresponding field without a second rule set',()=>{
 const validate=metadata(tickets,'getTicketDraftValidation')
 for(const [subject,body,field,message] of ticketCases){const result=validate(subject,body);assert.deepEqual(result,{field,message});assert.equal(tickets.validateTicketDraft(subject,body),result.message);assert.equal(Object.isFrozen(result),true)}
})
test('V03 Extension metadata keeps direct-call semantics without implicit normalization or reordered errors',()=>{
 const validate=metadata(extension,'getExtensionConfigValidation')
 for(const [value,field,message] of extensionCases){const result=validate(value);assert.deepEqual(result,{field,message});assert.equal(extension.validateExtensionConfig(value),result.message);assert.equal(Object.isFrozen(result),true)}
 assert.throws(()=>validate(undefined),TypeError)
})
test('V04 frozen metadata is only safe field/message primitives, with no input references, mutation or private values',()=>{
 const ticket=metadata(tickets,'getTicketDraftValidation'),ext=metadata(extension,'getExtensionConfigValidation')
 const source=Object.freeze(config(15,'','PRIVATE_SYNTHETIC_VALUE')),before={...source}
 const results=[ticket('','PRIVATE_SYNTHETIC_BODY'),ticket('valid',''),ext(source),ext(Object.freeze(config()))]
 assert.deepEqual(source,before)
 for(const result of results){assert.deepEqual(Object.keys(result).sort(),['field','message']);assert.equal(typeof result.field,'string');assert.equal(typeof result.message,'string');assert.ok(Object.isFrozen(result));assert.doesNotMatch(JSON.stringify(result),/PRIVATE_SYNTHETIC/)}
})
