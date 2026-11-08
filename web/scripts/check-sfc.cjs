const fs = require('node:fs');
const path = require('node:path');
const { parse, compileScript, compileTemplate, compileStyle } = require('@vue/compiler-sfc');

function collect(directory) {
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const filename = path.join(directory, entry.name);
    return entry.isDirectory() ? collect(filename) : filename.endsWith('.vue') ? [filename] : [];
  });
}

let failed = 0;
const files = process.argv.length > 2 ? process.argv.slice(2) : collect(path.resolve(__dirname, '../src'));
for (const filename of files) {
  const { descriptor, errors } = parse(fs.readFileSync(filename, 'utf8'), { filename });
  try {
    const script = descriptor.script || descriptor.scriptSetup
      ? compileScript(descriptor, { id: filename }) : null;
    if (descriptor.template) {
      errors.push(...compileTemplate({
        source: descriptor.template.content, filename, id: filename,
        compilerOptions: { bindingMetadata: script?.bindings },
      }).errors);
    }
    for (const style of descriptor.styles) {
      errors.push(...compileStyle({ source: style.content, filename, id: filename, scoped: style.scoped }).errors);
    }
  } catch (error) { errors.push(error); }
  if (errors.length) {
    failed++;
    console.error(path.relative(process.cwd(), filename), errors);
  }
}
console.log(`Vue SFC: ${files.length - failed}/${files.length} passed`);
if (failed) process.exitCode = 1;
