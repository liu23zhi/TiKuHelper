const {test}=require('node:test')
const assert=require('node:assert/strict')
const fs=require('node:fs'),os=require('node:os'),path=require('node:path')
const {spawnSync}=require('node:child_process')
const {createHash}=require('node:crypto')
const root=process.env.TIKU_PROJECT_ROOT||path.resolve(__dirname,'../..')
const dockerfile=fs.readFileSync(path.join(root,'Dockerfile'),'utf8')
// Execute the production shell-form RUN, not a second copy of its &&/|| logic.
const runtimeStage=dockerfile.replace(/\\\r?\n/g,'').split(/^\s*FROM\s+/im).at(-1)
const packageRuns=runtimeStage.split(/\r?\n/).filter(line=>/^\s*RUN\s+/.test(line)&&/\bapk\s+(?:update|upgrade|add)\b/.test(line))
assert.equal(packageRuns.length,1,'expected one runtime package-install RUN')
const command=packageRuns[0].replace(/^\s*RUN\s+/,'')
const fixture=`#!/bin/sh
case "\${0##*/}" in
 apk) step="apk $*" ;;
 update-ca-certificates) step="update-ca-certificates" ;;
 *) exit 90 ;;
esac
printf 'E02_STEP=%s\\n' "$step"
case "$step" in
 'apk update') failure=41 ;;
 'apk upgrade') failure=42 ;;
 'apk add --no-cache ca-certificates tzdata') failure=43 ;;
 'update-ca-certificates') failure=44 ;;
 *) printf 'unexpected package command: %s\\n' "$step" >&2; exit 91 ;;
esac
if [ "$TIKU_DOCKER_FAILURE_STEP" = "$step" ]; then
 printf 'E02_INJECTED_FAILURE=%s exit=%s\\n' "$step" "$failure" >&2
 exit "$failure"
fi
`
const cases=[
 {step:'apk update',status:41,stdout:'E02_STEP=apk update\n'},
 {step:'apk upgrade',status:42,stdout:'E02_STEP=apk update\nE02_STEP=apk upgrade\n'},
 {step:'apk add --no-cache ca-certificates tzdata',status:43,stdout:'E02_STEP=apk update\nE02_STEP=apk upgrade\nE02_STEP=apk add --no-cache ca-certificates tzdata\n'},
 {step:'update-ca-certificates',status:44,stdout:'E02_STEP=apk update\nE02_STEP=apk upgrade\nE02_STEP=apk add --no-cache ca-certificates tzdata\nE02_STEP=update-ca-certificates\n'},
 {step:'none',status:0,stdout:'E02_STEP=apk update\nE02_STEP=apk upgrade\nE02_STEP=apk add --no-cache ca-certificates tzdata\nE02_STEP=update-ca-certificates\n'}
]
for(const input of cases) test(input.status?`runtime package RUN fails closed at ${input.step} without executing later steps`:'runtime package RUN succeeds when all required steps succeed',t=>{
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'tiku-docker-failure-'))
 try{
  for(const file of ['apk','update-ca-certificates']) fs.writeFileSync(path.join(dir,file),fixture,{mode:0o755})
  const result=spawnSync('/bin/sh',['-c',command],{encoding:'utf8',timeout:10000,env:{...process.env,PATH:`${dir}:${process.env.PATH||'/usr/bin:/bin'}`,TIKU_DOCKER_FAILURE_STEP:input.step}})
  t.diagnostic(JSON.stringify({dockerfileSha256:createHash('sha256').update(dockerfile).digest('hex'),argv:['/bin/sh','-c',command],input:input.step,stdout:result.stdout,stderr:result.stderr,exitStatus:result.status,error:result.error?.message}))
  assert.ifError(result.error)
  assert.equal(result.signal,null)
  assert.equal(result.status,input.status,'the production RUN must propagate the failing required command')
  assert.equal(result.stdout,input.stdout,'later required commands must not execute after failure')
  assert.equal(result.stderr,input.status?`E02_INJECTED_FAILURE=${input.step} exit=${input.status}\n`:'','required-command diagnostics must remain visible')
 }finally{fs.rmSync(dir,{recursive:true,force:true})}
})
