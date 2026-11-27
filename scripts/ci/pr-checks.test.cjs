const {test}=require('node:test')
const assert=require('node:assert/strict')
const fs=require('node:fs'),path=require('node:path')
const yaml=require('js-yaml')
const root=process.env.TIKU_PROJECT_ROOT||path.resolve(__dirname,'../..')
const file=path.join(root,'.github/workflows/pr-checks.yml')
function workflow(){return yaml.load(fs.readFileSync(file,'utf8'))}
function commands(job){return (job.steps||[]).map(step=>step.run||'').join('\n')}
test('PR checks run for pull requests and manual dispatch, never privileged target events',()=>{
 const flow=workflow()
 assert.ok(Object.hasOwn(flow.on,'pull_request'))
 assert.ok(Object.hasOwn(flow.on,'workflow_dispatch'))
 assert.ok(!Object.hasOwn(flow.on,'pull_request_target'))
})
test('PR workflow grants read-only repository access',()=>{
 const flow=workflow()
 assert.equal(flow.permissions.contents,'read')
 assert.ok(Object.values(flow.permissions).every(v=>v==='read'||v==='none'))
})
test('frontend checks current source with locked install, complete tests and budgets',()=>{
 const flow=workflow(),job=flow.jobs.frontend,cmd=commands(job)
 for(const command of ['npm ci','npm run lint -- --no-fix','npm run test:security','npm run test:unit','npm run test:components','npm run check:sfc','npm run build','npm run check:bundle']) assert.ok(cmd.includes(command),command)
 assert.ok(job.steps.some(s=>s.with&&String(s.with['node-version'])==='22.23.1'))
})
test('Go checks embed this frontend result and cover short, vet and core race',()=>{
 const flow=workflow(),job=flow.jobs.backend,cmd=commands(job)
 assert.ok([job.needs].flat().includes('frontend'))
 const upload=flow.jobs.frontend.steps.find(s=>String(s.uses||'').startsWith('actions/upload-artifact@') && s.with?.name==='frontend-dist')
 assert.equal(upload?.with.path,'web/dist')
 assert.equal(upload?.with['if-no-files-found'],'error')
 const download=job.steps.findIndex(s=>String(s.uses||'').startsWith('actions/download-artifact@') && s.with?.name===upload.with.name && s.with.path===upload.with.path)
 const embed=job.steps.findIndex(s=>s.shell==='pwsh' && s.run?.trim()==='./scripts/prepare-web-dist.ps1 -SkipBuild -Json')
 const build=job.steps.findIndex(s=>s.run?.trim()==='go build ./...')
 assert.ok(download>=0 && download<embed && embed<build,'download this run’s exact frontend, sync it with the real helper, then build')
 assert.ok(commands(flow.jobs.frontend).includes('node --test ../scripts/prepare-web-dist.test.mjs'),'verify missing and successful real copies')
 assert.ok(cmd.includes('go test -short -p 1 ./... -count=1'))
 assert.ok(cmd.includes('go vet ./...'))
 assert.ok(cmd.includes('go test -short -race -p 1 ./internal/api ./internal/controller ./internal/service ./internal/search -count=1'))
})
test('backend integration creates its output directory and retains HTTP and S04 evidence',()=>{
 const steps=workflow().jobs.backend.steps
 const smoke=steps.find(s=>s.run?.includes('scripts/ci/http-smoke.py'))
 assert.match(smoke.run, /mkdir -p ci-output\n[^]*> ci-output\/http-smoke\.json/)
 const uploads=steps.filter(s=>s.uses?.startsWith('actions/upload-artifact@'))
 assert.equal(uploads.find(s=>s.with.name==='http-smoke-evidence')?.with.path,'ci-output/http-smoke.json')
 const s04=steps.find(s=>s.run?.includes('scripts/ci/run-s04-main-flow.py'))
 const evidence=uploads.find(s=>s.with.name==='s04-main-flow-evidence')
 assert.ok(evidence,'upload structured S04 evidence')
 assert.match(evidence.if,new RegExp(`steps\\.${s04.id}\\.outcome == 'failure'`))
 assert.deepEqual(evidence.with.path.trim().split(/\r?\n/),[
  'ci-output/s04-main-flow/LATEST.json',
  'ci-output/s04-main-flow/attempt-*/RESULT.json',
  'ci-output/s04-main-flow/attempt-*/*/RESULT.json'
 ])
})
test('workflow jobs and steps have Chinese display names',()=>{
 const flow=workflow(),hasChinese=value=>/[\u3400-\u9fff]/.test(value||'')
 assert.ok(hasChinese(flow.name))
 for(const job of Object.values(flow.jobs)){
  assert.ok(hasChinese(job.name),'job display name should be Chinese')
  for(const step of job.steps) assert.ok(hasChinese(step.name),'step display name should be Chinese')
 }
})
test('dependency audit has an explicit checked policy rather than swallowed failures',()=>{
 const flow=workflow(),cmd=commands(flow.jobs.audit)
 assert.ok(cmd.includes('npm audit --json'))
 assert.ok(cmd.includes('check-audit'))
 assert.ok(!cmd.includes('|| true'))
})
test('PR checks do not read deployment secrets or publish images',()=>{
 const flow=workflow(),source=JSON.stringify(flow)
 for(const forbidden of ['secrets.','pull_request_target','docker/login-action','docker/build-push-action']) assert.ok(!source.includes(forbidden),forbidden)
 assert.ok(!/docker\s+(?:push|login)\b/.test(source))
})
