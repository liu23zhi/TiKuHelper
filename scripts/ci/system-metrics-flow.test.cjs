const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),os=require('node:os'),cp=require('node:child_process'),Module=require('node:module')
const root=process.env.TIKU_PROJECT_ROOT||path.resolve(__dirname,'../..')
process.env.NODE_PATH=[path.join(root,'web/node_modules'),process.env.NODE_PATH].filter(Boolean).join(path.delimiter);Module._initPaths()
const yaml=require('js-yaml'),runner=path.join(root,'web/scripts/run-system-metrics-flow.cjs')
test('metrics flow is an unconditional backend gate using the existing locked UI install',()=>{
 const flow=yaml.load(fs.readFileSync(path.join(root,'.github/workflows/pr-checks.yml'),'utf8')),steps=flow.jobs.backend.steps
 const index=steps.findIndex(s=>s.run==='npm run --prefix web test:integration:metrics')
 assert.ok(index>=0,'missing required producer/API/native metrics join')
 assert.equal(steps[index]['continue-on-error'],undefined);assert.equal(steps[index].if,undefined)
 assert.ok(steps.slice(0,index).some(s=>String(s.run).includes('npm ci --prefix web')),'reuse this job locked dependencies')
 const pkg=JSON.parse(fs.readFileSync(path.join(root,'web/package.json'),'utf8'))
 assert.equal(pkg.scripts['test:integration:metrics'],'node scripts/run-system-metrics-flow.cjs')
})
function boundary(goBody,nodeExit){
 assert.ok(fs.existsSync(runner),'missing required metrics orchestrator; runtime sub-boundaries not yet exercised')
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'tikuhelper-metrics-gate-')),go=path.join(dir,'go-fixture'),node=path.join(dir,'node-fixture'),marker=path.join(dir,'node-ran')
 try{
  fs.writeFileSync(go,`#!${process.execPath}\n${goBody}\n`,{mode:0o755})
  fs.writeFileSync(node,`#!${process.execPath}\nrequire('fs').writeFileSync(${JSON.stringify(marker)},'called');process.exit(${nodeExit})\n`,{mode:0o755})
  const result=cp.spawnSync(process.execPath,[runner],{cwd:root,env:{...process.env,TIKU_METRICS_FLOW_GO_BINARY:go,TIKU_METRICS_FLOW_NODE_BINARY:node,TIKU_METRICS_FLOW_EVIDENCE:''},encoding:'utf8',timeout:15000})
  assert.equal(result.error,undefined);return{status:result.status,nodeRan:fs.existsSync(marker),stdout:result.stdout,stderr:result.stderr}
 }finally{
  assert.equal(path.dirname(fs.realpathSync(dir)),fs.realpathSync(os.tmpdir()));assert.ok(path.basename(dir).startsWith('tikuhelper-metrics-gate-'))
  fs.rmSync(dir,{recursive:true,force:false})
 }
}
test('producer failure or missing typed output fails closed before the native consumer',()=>{
 const failed=boundary('process.exit(23)',0);assert.equal(failed.status,23);assert.equal(failed.nodeRan,false)
 const absent=boundary('process.exit(0)',0);assert.notEqual(absent.status,0);assert.equal(absent.nodeRan,false)
})
test('native consumer failure is propagated, never converted to a successful gate',()=>{
 const body="require('fs').writeFileSync(process.env.TIKU_METRICS_FLOW_OUTPUT,JSON.stringify({schemaVersion:1,cases:['empty','populated','forbidden'].map(name=>({name,status:name==='forbidden'?403:200,headers:{},body:{}}))}));process.exit(0)"
 const result=boundary(body,29);assert.equal(result.status,29);assert.equal(result.nodeRan,true)
})
