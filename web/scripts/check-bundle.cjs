const fs=require('node:fs'),path=require('node:path'),zlib=require('node:zlib'),crypto=require('node:crypto')
const root=path.resolve(process.argv[2]||'dist')
const html=fs.readFileSync(path.join(root,'index.html'),'utf8')
const attrs=tag=>Object.fromEntries([...tag.matchAll(/([\w-]+)=["']([^"']*)["']/g)].map(m=>[m[1],m[2]]))
const scripts=[...html.matchAll(/<script\b[^>]*>/gi)].map(m=>attrs(m[0]).src).filter(Boolean)
const links=[...html.matchAll(/<link\b[^>]*>/gi)].map(m=>attrs(m[0]))
const styles=links.filter(x=>x.rel==='stylesheet').map(x=>x.href)
const prefetch=links.filter(x=>x.rel==='prefetch')
const assets=[...new Set([...scripts,...styles])].map(url=>{
  const name=new URL(url,'http://fixture').pathname
  const file=path.resolve(root,'.'+name)
  if(!file.startsWith(root+path.sep))throw new Error('Asset escaped dist')
  const bytes=fs.readFileSync(file)
  return {path:name,bytes:bytes.length,gzip:zlib.gzipSync(bytes).length,sha256:crypto.createHash('sha256').update(bytes).digest('hex')}
})
const js=assets.filter(x=>x.path.endsWith('.js')),css=assets.filter(x=>x.path.endsWith('.css'))
const limits={initialJsGzip:420*1024,appJsGzip:40*1024,initialCssGzip:70*1024,prefetchCount:0}
const totals={initialJsGzip:js.reduce((a,x)=>a+x.gzip,0),appJsGzip:js.filter(x=>/\/app\./.test(x.path)).reduce((a,x)=>a+x.gzip,0),initialCssGzip:css.reduce((a,x)=>a+x.gzip,0),prefetchCount:prefetch.length}
const violations=Object.keys(limits).filter(key=>totals[key]>limits[key]).map(key=>({metric:key,actual:totals[key],limit:limits[key]}))
console.log(JSON.stringify({assets,totals,limits,violations,scope:'Initial referenced scripts and styles, gzip model; not observed transport compression or LCP'},null,2))
process.exitCode=violations.length?1:0
