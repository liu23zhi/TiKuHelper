import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const script = join(repo, 'run', 'nginx', 'trusted-proxies-convert.sh')
const isWindows = process.platform === 'win32'
const shell = isWindows ? 'bash.exe' : 'sh'

function wslPath(value) {
  return value.replace(/^([A-Za-z]):[\\/]/, (_, drive) => `/mnt/${drive.toLowerCase()}/`).replaceAll('\\', '/')
}

function checkShell(t) {
  const args = isWindows ? ['-lc', 'command -v awk && command -v grep && command -v sed && command -v tr && command -v mktemp']
    : ['-c', 'command -v awk && command -v grep && command -v sed && command -v tr && command -v mktemp']
  const probe = spawnSync(shell, args, { encoding: 'utf8', timeout: 30000 })
  if (probe.status !== 0) {
    t.skip('A POSIX shell and the converter utilities are unavailable')
    return false
  }
  return true
}

function convert(source, output) {
  if (isWindows) {
    const command = `sh '${wslPath(script)}' '${wslPath(source)}' '${wslPath(output)}'`
    return spawnSync(shell, ['-lc', command], { encoding: 'utf8', timeout: 30000 })
  }
  return spawnSync(shell, [script, source, output], { encoding: 'utf8', timeout: 30000 })
}

test('Nginx converter preserves the source format and emits realip directives', async (t) => {
  if (!checkShell(t)) return
  const directory = await mkdtemp(join(tmpdir(), 'tiku-proxy-converter-'))
  const source = join(directory, 'trusted_proxies.txt')
  const output = join(directory, 'trusted-proxies.generated.conf')
  const original = '# unchanged\ntrusted_proxies: ["172.26.0.3", "10.0.0.0/255.0.0.0"]\n172.26.0.10-172.26.0.12\n'
  try {
    await writeFile(source, original)
    const result = convert(source, output)
    assert.equal(result.status, 0, result.stderr)
    assert.equal(await readFile(source, 'utf8'), original)
    assert.match(await readFile(output, 'utf8'), /set_real_ip_from 172\.26\.0\.3;/)
    assert.match(await readFile(output, 'utf8'), /set_real_ip_from 10\.0\.0\.0\/8;/)
    assert.match(await readFile(output, 'utf8'), /set_real_ip_from 172\.26\.0\.10\/31;/)
    assert.match(await readFile(output, 'utf8'), /set_real_ip_from 172\.26\.0\.12\/32;/)
    const generated = await readFile(output, 'utf8')
    for (const subnet of ['127.0.0.0/8', '::1/128', '10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', '169.254.0.0/16', 'fc00::/7', 'fe80::/10']) {
      assert.match(generated, new RegExp(`set_real_ip_from ${subnet.replaceAll('.', '\\.').replace('/', '\\/')};`))
    }
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('empty or comment-only trusted proxy input emits trust-all realip directives', async (t) => {
  if (!checkShell(t)) return
  const directory = await mkdtemp(join(tmpdir(), 'tiku-proxy-empty-'))
  const source = join(directory, 'trusted_proxies.txt')
  const output = join(directory, 'trusted-proxies.generated.conf')
  const original = '# only comments\n\n'
  try {
    await writeFile(source, original)
    const result = convert(source, output)
    assert.equal(result.status, 0, result.stderr)
    assert.equal(await readFile(source, 'utf8'), original)
    assert.deepEqual((await readFile(output, 'utf8')).trim().split(/\r?\n/), [
      'set_real_ip_from 0.0.0.0/0;',
      'set_real_ip_from ::/0;',
    ])
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

test('missing trusted proxy input follows the application empty-file default', async (t) => {
  if (!checkShell(t)) return
  const directory = await mkdtemp(join(tmpdir(), 'tiku-proxy-missing-'))
  const source = join(directory, 'missing.txt')
  const output = join(directory, 'trusted-proxies.generated.conf')
  try {
    const result = convert(source, output)
    assert.equal(result.status, 0, result.stderr)
    assert.deepEqual((await readFile(output, 'utf8')).trim().split(/\r?\n/), [
      'set_real_ip_from 0.0.0.0/0;',
      'set_real_ip_from ::/0;',
    ])
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

