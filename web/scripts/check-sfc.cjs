const fs = require('node:fs')
const path = require('node:path')
const { parse, compileScript, compileTemplate } = require('@vue/compiler-sfc')
function vueFiles(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap(entry => entry.isDirectory() ? vueFiles(path.join(dir, entry.name)) : entry.name.endsWith('.vue') ? [path.join(dir, entry.name)] : [])
}
const targets = process.argv.length > 2 ? process.argv.slice(2) : vueFiles('src')
for (const filename of targets) {
  const { descriptor, errors } = parse(fs.readFileSync(filename, 'utf8'), { filename })
  if (errors.length) throw new Error(filename + ': ' + errors.join('; '))
  const id = 'integration-sfc'
  const script = descriptor.script || descriptor.scriptSetup ? compileScript(descriptor, { id }) : null
  const result = compileTemplate({ source: descriptor.template?.content || '', filename, id, scoped: descriptor.styles.some(style => style.scoped), compilerOptions: { bindingMetadata: script?.bindings } })
  if (result.errors.length) throw new Error(filename + ': ' + result.errors.join('; '))
  console.log('PASS parse + compileScript + compileTemplate ' + filename)
}
