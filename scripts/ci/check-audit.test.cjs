const {test}=require('node:test')
const assert=require('node:assert/strict')
const path=require('node:path')
function evaluate(report,policy={schemaVersion:1,failAt:'high',exceptions:[]},exit=0){
 const root=process.env.TIKU_PROJECT_ROOT||path.resolve(__dirname,'../..')
 return require(path.join(root,'scripts/ci/check-audit.cjs')).evaluateAudit(report,policy,exit)
}
function report(severity){
 const counts={info:0,low:0,moderate:0,high:0,critical:0,total:0}
 const vulnerabilities={}
 if(severity){counts[severity]=1;counts.total=1;vulnerabilities.fixture={name:'fixture',severity,via:[],nodes:['node_modules/fixture'],range:'*'}}
 return {auditReportVersion:2,vulnerabilities,metadata:{vulnerabilities:counts}}
}
test('a complete clean audit passes',()=>assert.equal(evaluate(report()).decision,'pass'))
test('moderate findings remain visible but do not cross the high threshold',()=>{
 const result=evaluate(report('moderate'),undefined,1)
 assert.equal(result.decision,'pass');assert.equal(result.counts.moderate,1)
})
test('high risk blocks the gate',()=>assert.equal(evaluate(report('high'),undefined,1).decision,'block'))
test('critical risk blocks the gate',()=>assert.equal(evaluate(report('critical'),undefined,1).decision,'block'))
test('registry errors are not interpreted as a clean report',()=>{
 assert.throws(()=>evaluate({error:{code:'ENOTFOUND'}}),/valid audit report/)
})
test('missing metadata fails closed',()=>{
 assert.throws(()=>evaluate({auditReportVersion:2,vulnerabilities:{}}),/valid audit report/)
})
test('unknown severities fail closed',()=>{
 const data=report('high');data.vulnerabilities.fixture.severity='unknown'
 assert.throws(()=>evaluate(data,undefined,1),/severity/)
})
test('mismatched package counts fail closed',()=>{
 const data=report();data.metadata.vulnerabilities.total=1
 assert.throws(()=>evaluate(data),/count/)
})
test('npm transport and report exit inconsistencies fail closed',()=>{
 assert.throws(()=>evaluate(report(),undefined,2),/npm exit/)
 assert.throws(()=>evaluate(report(),undefined,1),/npm exit/)
})
test('unapproved exceptions cannot waive findings',()=>{
 assert.throws(()=>evaluate(report('high'),{schemaVersion:1,failAt:'high',exceptions:['*']},1),/exception/)
})
