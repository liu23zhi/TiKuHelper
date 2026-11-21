const {test}=require('node:test')
const assert=require('node:assert/strict')
const fs=require('node:fs'),path=require('node:path')
const root=process.env.TIKU_PROJECT_ROOT||path.resolve(__dirname,'../..')
const source=fs.readFileSync(path.join(root,'Dockerfile'),'utf8')
const expected={
  "node:26-alpine": "sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216",
  "golang:1.27.3": "sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e",
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
// A pin update must not change any executable instruction. Unlike hashing comments and
// line wrapping, this diff names the exact changed stage/command. The reviewed contract
// includes the frontend check before go build, the fail-closed tool option and runtime.
const approvedInstructions = [
  [
    "WORKDIR /build",
    "COPY ./web .",
    "RUN npm ci && npm run build"
  ],
  [
    "ENV GO111MODULE=on  CGO_ENABLED=0  GOOS=linux",
    "WORKDIR /build",
    "ADD go.mod go.sum ./",
    "RUN go mod download",
    "COPY . .",
    "COPY --from=builder1 /build/dist ./cmd/adapter-service/dist",
    "RUN test -f cmd/adapter-service/dist/index.html  && grep -q 'id=\"app\"' cmd/adapter-service/dist/index.html  && echo \"嵌入前端产物：$(find cmd/adapter-service/dist -type f | wc -l) 个文件\"",
    "RUN go build -ldflags \"-s -w -extldflags '-static'\" -o tiku-adapter ./cmd/adapter-service"
  ],
  [
    "RUN apk update  && apk upgrade  && apk add --no-cache ca-certificates tzdata  && update-ca-certificates",
    "ENV TZ=Asia/Shanghai",
    "COPY --from=builder2 /build/tiku-adapter /app/tiku-adapter",
    "ARG WITH_USEREXAM_TOOL=1",
    "RUN --mount=type=bind,source=tools/userexam-crypto/dist,target=/src,ro  if [ \"$WITH_USEREXAM_TOOL\" = \"1\" ]; then  test -s /src/userexam-crypto.exe || {  echo \"缺少脱机工具：请先运行 tools/userexam-crypto/build-exe.ps1 生成 dist/userexam-crypto.exe，\" >&2;  echo \"或显式声明 --build-arg WITH_USEREXAM_TOOL=0 以跳过内嵌（管理台会如实显示未部署）。\" >&2;  exit 1;  };  mkdir -p /app && cp /src/userexam-crypto.exe /app/userexam-crypto.exe  && chmod 0755 /app/userexam-crypto.exe;  else  echo \"WITH_USEREXAM_TOOL=0：跳过内嵌脱机工具，管理台将显示「未部署脱机工具」。\" >&2;  fi",
    "ENV USEREXAM_TOOL_PATH=/app/userexam-crypto.exe",
    "RUN mkdir -p /app/data",
    "EXPOSE 8060",
    "WORKDIR /app",
    "VOLUME [\"/app/data\"]",
    "ENTRYPOINT [\"/app/tiku-adapter\"]"
  ]
]
test('pinned stages retain their reviewed build, embed and runtime instructions in order', () => {
 const stages = []
 const logical = source.replace(/^\s*#.*$/gm, '').replace(/\\\r?\n[ \t]*/g, ' ')
 for (const line of logical.split(/\r?\n/).map(s => s.trim()).filter(Boolean)) {
  if (/^FROM /i.test(line)) stages.push([])
  else { assert.ok(stages.length, 'instruction before first stage'); stages.at(-1).push(line) }
 }
 assert.deepEqual(stages, approvedInstructions)
})
