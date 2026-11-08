const {test}=require('node:test')
const assert=require('node:assert/strict')
const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto')
const root=process.env.TIKU_PROJECT_ROOT||path.resolve(__dirname,'../..')
const source=fs.readFileSync(path.join(root,'Dockerfile'),'utf8')
const expected={
  "node:26-alpine": "sha256:c6ad9e9c1d80f6eb607c26b4b0a132ff7a2344fad1c78ea3baa2a7d4c8de6d94",
  "golang:1.27.3": "sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b",
  "alpine:3.24": "sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a"
}
const froms=source.split(/\r?\n/).filter(line=>/^FROM /i.test(line))
for(const [i,[tag,digest]] of Object.entries(expected).entries()) {
 test('Docker base retains '+tag+' at the verified multiarch index digest',()=>{
  assert.equal(froms.length,3)
  const expectedSuffix=i===0?' AS builder1':i===1?' AS builder2':''
  assert.equal(froms[i],'FROM '+tag+'@'+digest+expectedSuffix)
  assert.match(digest,/^sha256:[0-9a-f]{64}$/)
  assert.ok(!/--platform/i.test(froms[i]),'preserve platform selection, do not narrow to amd64 leaf')
 })
}
test('pinning changes only FROM digests; original stages commands and runtime policy remain exact',()=>{
 const stripped=source.replace(/(FROM (?:node:26-alpine|golang:1\.27\.3|alpine:3\.24))@sha256:[0-9a-f]{64}/g,'$1')
 assert.equal(crypto.createHash('sha256').update(stripped).digest('hex'),'e4abb2e88fc661e19861e98c5eb4ac5d90bf3e6e5464ade68fa12c9e5c7b86c3')
})
