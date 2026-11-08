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
 assert.ok(job.steps.some(s=>String(s.uses||'').includes('download-artifact')))
 assert.ok(cmd.includes('cmd/adapter-service/dist'))
 assert.ok(cmd.includes('go test -short -p 1 ./... -count=1'))
 assert.ok(cmd.includes('go vet ./...'))
 assert.ok(cmd.includes('go test -short -race -p 1 ./internal/api ./internal/controller ./internal/service ./internal/search -count=1'))
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
