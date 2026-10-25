const test=require('node:test'),assert=require('node:assert/strict'),path=require('node:path'),fs=require('node:fs'),os=require('node:os'),http=require('node:http'),zlib=require('node:zlib')
const {createRequire}=require('node:module')
const root=process.env.FRONTEND_INSTALLED_WEB_ROOT||path.resolve(__dirname,'../..'),req=createRequire(path.join(root,'package.json'))
const later=()=>new Promise(resolve=>setImmediate(resolve))
function open(server){return new Promise(resolve=>server.listen(0,'127.0.0.1',()=>resolve(server.address().port)))}
function close(server){return new Promise(resolve=>server.close(resolve))}
function get(port,url,headers={}){return new Promise((resolve,reject)=>{const q=http.get({hostname:'127.0.0.1',port,path:url,headers,agent:false},r=>{const data=[];r.on('data',d=>data.push(d));r.on('end',()=>resolve({status:r.statusCode,headers:r.headers,body:Buffer.concat(data)}))});q.on('error',reject);q.setTimeout(5000,()=>q.destroy(new Error('fixture request timeout')))})}
test('security proxy-addr rejects spoofed XFF when a short IPv4-mapped IPv6 subnet is configured',()=>{
 const proxyaddr=req('proxy-addr'),request={connection:{remoteAddress:'203.0.113.10'},headers:{'x-forwarded-for':'198.51.100.25'}}
 let trust;try{trust=proxyaddr.compile(['::ffff:10.0.0.0/8'])}catch(error){assert.ok(error instanceof Error,'invalid subnet may fail closed');return}
 assert.equal(trust('203.0.113.10',0),false,'short mapped prefix must never trust an unrelated IPv4 peer')
 assert.equal(proxyaddr(request,trust),'203.0.113.10','untrusted direct peer must win over spoofed forwarded header')
})
test('security proxy-addr preserves intended ordinary trusted and untrusted IPv4 chains',()=>{
 const proxyaddr=req('proxy-addr'),trust=proxyaddr.compile(['10.0.0.0/8'])
 assert.equal(proxyaddr({connection:{remoteAddress:'10.0.0.5'},headers:{'x-forwarded-for':'198.51.100.25'}},trust),'198.51.100.25')
 assert.equal(proxyaddr({connection:{remoteAddress:'203.0.113.10'},headers:{'x-forwarded-for':'198.51.100.25'}},trust),'203.0.113.10')
})
test('security qs safely stringifies its own parsed attacker constructor isBuffer object',()=>{
 const qs=req('qs'),value=qs.parse('a[constructor][isBuffer]=not-callable',{plainObjects:true})
 assert.doesNotThrow(()=>qs.stringify(value),'untrusted constructor.isBuffer must not become a callable')
 assert.deepEqual(qs.parse('course=one&course=two'),{course:['one','two']})
})
test('security serializer does not terminate an HTML script through a bare function-source closing tag',()=>{
 const serialize=req('serialize-javascript'),{JSDOM}=req('jsdom')
 function first(){}first.toString=()=> 'function first(){ return 1 }//</script'
 function second(){}second.toString=()=> 'function second(){ return 2 } > <img data-injected="fixture">'
 const result=serialize({a:{first},second},{space:2})
 const dom=new JSDOM('<!doctype html><script>'+result+'</script>')
 try{assert.equal(dom.window.document.querySelector('[data-injected]'),null,'serialized function boundary must remain inside script text')}finally{dom.window.close()}
 const plain={text:'</script>',date:new Date('2026-01-01T00:00:00Z'),list:[1,true,null]};const json=serialize(plain,{isJSON:true});assert.deepEqual(JSON.parse(json),JSON.parse(JSON.stringify(plain)));assert.equal(json.includes('</script>'),false)
})
test('security source-map-js retains generated-to-original mapping API',()=>{
 const {SourceMapGenerator,SourceMapConsumer}=req('source-map-js');const g=new SourceMapGenerator({file:'out.css'});g.addMapping({generated:{line:1,column:0},original:{line:4,column:2},source:'input.css'});g.setSourceContent('input.css','.x { color: red }');const c=new SourceMapConsumer(g.toJSON());assert.deepEqual(c.originalPositionFor({line:1,column:0}),{source:'input.css',line:4,column:2,name:null})
})
test('security Yorkie cross-spawn consumer retains argument boundaries on the real Node fixture',()=>{
 const yorkie=createRequire(req.resolve('yorkie/package.json')),spawn=yorkie('cross-spawn');const value='literal with spaces & symbols';const p=spawn.sync(process.execPath,['-e','process.stdout.write(JSON.stringify(process.argv.slice(1)))',value],{encoding:'utf8',timeout:5000});assert.ifError(p.error);assert.equal(p.status,0);assert.deepEqual(JSON.parse(p.stdout),[value])
})
test('security patched legacy compiler-utils still compiles real scoped style',()=>{
 const {compileStyle}=req('@vue/component-compiler-utils');const out=compileStyle({source:'.probe { color: red; }',filename:'/tmp/controlled.vue',id:'data-v-graph',scoped:true});assert.deepEqual(out.errors,[]);assert.match(out.code,/\.probe\[data-v-graph\]/);assert.match(out.code,/color:\s*red/)
})
test('security webpack middleware contains output even with a non-slash-terminated publicPath',async()=>{
 const webpack=req('webpack'),middlewareFactory=req('webpack-dev-middleware'),dir=fs.mkdtempSync(path.join(os.tmpdir(),'wdm-contained-'));const entry=path.join(dir,'entry.js');fs.writeFileSync(entry,'module.exports = 42;');const compiler=webpack({mode:'development',entry,output:{path:path.join(dir,'out'),filename:'bundle.js',publicPath:'/assets'}});const middleware=middlewareFactory(compiler,{publicPath:'/assets',stats:false});const server=http.createServer((q,s)=>middleware(q,s,()=>{s.statusCode=404;s.end('not found')}))
 try{await new Promise(r=>middleware.waitUntilValid(r));compiler.outputFileSystem.writeFileSync(path.join(dir,'outside.js'),'CONTROLLED_OUTSIDE_FIXTURE');const port=await open(server);const ordinary=await get(port,'/assets/bundle.js');assert.equal(ordinary.status,200);const escape=await get(port,'/assets../outside.js');assert.notEqual(escape.body.toString(),'CONTROLLED_OUTSIDE_FIXTURE','prefix stripping must not escape output root');assert.equal(escape.status,403)}finally{await close(server);await new Promise(r=>middleware.close(r));await new Promise(r=>compiler.close(r));fs.rmSync(dir,{recursive:true,force:true})}
})
test('security compression destroys the real gzip stream after a premature response close',async()=>{
 const desc=Object.getOwnPropertyDescriptor(zlib,'createGzip'),streams=[];Object.defineProperty(zlib,'createGzip',{...desc,value(...args){const s=desc.value(...args);streams.push(s);return s}});let server;let tick;let responseClosed;const closed=new Promise(r=>{responseClosed=r})
 try{const compression=req('compression')({threshold:0});server=http.createServer((q,s)=>compression(q,s,()=>{s.setHeader('Content-Type','text/plain');tick=setInterval(()=>{s.write('controlled-stream-data\n'.repeat(512));s.flush()},2);s.on('close',()=>{clearInterval(tick);responseClosed()})}));const port=await open(server);await new Promise((resolve,reject)=>{const q=http.get({hostname:'127.0.0.1',port,headers:{'accept-encoding':'gzip'},agent:false},s=>s.once('data',()=>{s.destroy();q.destroy();resolve()}));q.on('error',e=>e.code==='ECONNRESET'?resolve():reject(e))});await closed;await later();await later();assert.equal(streams.length,1);assert.equal(streams[0].destroyed,true,'aborted response must release its native zlib stream')}finally{clearInterval(tick);if(server)await close(server);for(const s of streams)s.destroy();Object.defineProperty(zlib,'createGzip',desc)}
})
