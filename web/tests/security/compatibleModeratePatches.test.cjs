const {test,after}=require('node:test')
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto'),Module=require('node:module')
const {createRequire}=Module
const depRoot=path.resolve(process.env.DEPENDENCY_NODE_MODULES||path.resolve(__dirname,'../../node_modules'))
const web=path.resolve(process.env.FRONTEND_SOURCE_WEB||path.resolve(__dirname,'../..'))
const req=createRequire(path.join(depRoot,'__local_compatibility__.cjs'))
const ledgerPath=process.env.COMPATIBILITY_LEDGER
const records=[];let current
const sha=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')
const resolvedRoot=fs.realpathSync(depRoot)
const info=name=>{const file=req.resolve(name),p=req.resolve(name+'/package.json');return{version:JSON.parse(fs.readFileSync(p)).version,entry:fs.realpathSync(file),entrySHA256:sha(file),packageSHA256:sha(p)}}
const modules={qs:info('qs'),parser:info('postcss-selector-parser')}
const fixtureHash=sha(__filename)
const sourceFile=path.join(web,'src/api/api.js'),sourceHash=sha(sourceFile)
function check(value,label){current.checks.push({label,passed:Boolean(value)})}
function record(name,run){test(name,async()=>{current={name,checks:[],passed:false};try{await run();assert.deepEqual(current.checks.filter(x=>!x.passed).map(x=>x.label),[],name+' compatibility');current.passed=true}catch(error){current.error={name:error.name,message:error.message};throw error}finally{records.push(current)}})}
function writeLedger(){if(ledgerPath)fs.writeFileSync(ledgerPath,JSON.stringify({schema:'LOCAL_PACKAGE_COMPATIBILITY_V1',fixtureSHA256:fixtureHash,depRoot,realDepRoot:resolvedRoot,modules,sourceFile,sourceSHA256:sourceHash,sourceStable:sha(sourceFile)===sourceHash,cases:records,counts:{tests:records.length,pass:records.filter(x=>x.passed).length,fail:records.filter(x=>!x.passed).length},networkRequests:0,providerOrDatabase:0,versionOrTimingUsedAsFailure:false},null,2)+'\n')}
after(writeLedger)

record('MC01 qs ordinary serialization, preserved constructor case and bounded comma data handling',async()=>{
 const qs=req('qs')
 check(modules.qs.entry.startsWith(resolvedRoot+path.sep),'actual qs entry resolves inside selected real dependency tree')
 const value=qs.parse('a[constructor][isBuffer]=not-callable',{plainObjects:true})
 assert.doesNotThrow(()=>qs.stringify(value),'untrusted constructor.isBuffer must not become a callable')
 assert.deepEqual(qs.parse('course=one&course=two'),{course:['one','two']})
 check(true,'original constructor.isBuffer and repeated-course assertions preserved')
 const ordinary=qs.stringify({a:['one','two']},{arrayFormat:'comma'})
 check(qs.parse(ordinary).a==='one,two','ordinary comma serialization still roundtrips two ordinary strings')
 const dataObjects=[{toString:'text'},Object.assign(Object.create(null),{label:'one'})]
 current.objectResults=[]
 for(const [index,item]of dataObjects.entries())for(const encodeValuesOnly of [false,true]){
  let output,error
  try{output=qs.stringify({a:[item,'tail']},{arrayFormat:'comma',encodeValuesOnly})}catch(e){error=e}
  check(!error&&typeof output==='string','bounded comma plain data object '+index+' encodeValuesOnly='+encodeValuesOnly+' has a defined string result')
  current.objectResults.push({index,encodeValuesOnly,threw:Boolean(error),errorName:error?.name||null,resultBytes:typeof output==='string'?Buffer.byteLength(output):null})
 }
 const html='<p>ordinary & 中文 = input</p>'
 check(new URLSearchParams(qs.stringify({html})).get('html')===html,'form-encoded html ordinary bytes are preserved')
})

record('MC02 actual reParseFile source uses selected qs form encoding and its original transport contract',async()=>{
 const axios=req('axios'),babel=req('@babel/core'),previous=axios.defaults.adapter,calls=[],result={paper:{title:'LOCAL_COMPATIBILITY'}}
 axios.defaults.adapter=async config=>{calls.push(config);return{status:200,data:result,headers:{},config}}
 try{
  const code=babel.transformSync(fs.readFileSync(sourceFile,'utf8'),{filename:sourceFile,babelrc:false,configFile:false,plugins:[req.resolve('@babel/plugin-transform-modules-commonjs',{paths:[req.resolve('@vue/babel-preset-app')]})]}).code
  const m=new Module(sourceFile,module);m.filename=sourceFile;m.paths=module.paths
  m.require=id=>{if(id==='axios')return axios;if(id==='qs')return req('qs');if(id==='../utils/requestRetry.mjs')return{executeWithRetry:(fn,config)=>fn(config),toUserRequestError:(_config,error)=>error};throw new Error('Unexpected local source import '+id)}
  m._compile(code,sourceFile)
  const html='<p>local form & 中文 = input</p>',value=await m.exports.reParseFile({html})
  check(value===result,'actual successful raw response identity is retained')
  check(calls.length===1,'actual source dispatches one explicitly mocked transport only')
  const call=calls[0]
  check(call.baseURL==='/sqp/api'&&call.url==='/parse'&&call.method==='post'&&call.timeout===60000&&call.retryPolicy==='single','actual source baseURL/POST/timeout/single policy preserved')
  check(new URLSearchParams(String(call.data)).get('html')===html,'actual source qs-produced form body retains full html text')
  current.transport={calls:calls.length,actualAxiosMemory:true,actualSourceEvaluated:true,retrySeam:'Direct single-attempt callback only; retry algorithm itself unchanged/not qualified by this test.'}
 }finally{axios.defaults.adapter=previous}
})

record('MC03 root parser6 real selector consumer and unchanged separate scoped-style compiler stay compatible',async()=>{
 const parser=req('postcss-selector-parser')
 check(modules.parser.entry.startsWith(resolvedRoot+path.sep),'actual root parser entry resolves inside selected real dependency tree')
 for(const selector of ['.ordinary > #entry[data-label="one"]',':is(.one,.two) + .next','.x'.repeat(256)]){
  const ast=parser().astSync(selector)
  check(ast.toString()===selector,'bounded selector roundtrip '+Buffer.byteLength(selector)+' bytes remains exact')
 }
 const pluginEntry=req.resolve('postcss-minify-selectors'),consumerReq=createRequire(pluginEntry),actualParser=fs.realpathSync(consumerReq.resolve('postcss-selector-parser'))
 check(actualParser===modules.parser.entry,'actual minify-selector consumer resolves the selected root parser6')
 const postcss=req('postcss'),minify=req('postcss-minify-selectors'),out=await postcss([minify()]).process('.ordinary > .next { color: red; }',{from:undefined})
 check(out.css.includes('color: red')||out.css.includes('color:red'),'actual selector-minify consumer preserves declaration')
 const {compileStyle}=req('@vue/component-compiler-utils')
 const style=compileStyle({source:'.probe { color: red; }',filename:'/tmp/controlled.vue',id:'data-v-graph',scoped:true})
 assert.deepEqual(style.errors,[])
 assert.match(style.code,/\.probe\[data-v-graph\]/)
 assert.match(style.code,/color:\s*red/)
 check(true,'original scoped-style compiler assertions preserved separately from root parser6')
 current.selectorConsumers={rootParser:modules.parser.entry,minifyConsumer:pluginEntry,scopedCompiler:req.resolve('@vue/component-compiler-utils'),boundedOnly:true,noPerformanceThreshold:true}
})
