import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const script = join(repo, 'run', 'nginx', 'trusted-proxies-convert.sh')

function wslPath(value) {
  return value.replace(/^([A-Za-z]):[\\/]/, (_, drive) => `/mnt/${drive.toLowerCase()}/`).replaceAll('\\', '/')
}

test('Nginx converter preserves the source format and emits realip directives', async (t) => {
  const probe = spawnSync('bash.exe', ['-lc', 'command -v awk && command -v grep'], { encoding: 'utf8', timeout: 30000 })
  if (probe.status !== 0) return t.skip('bash/WSL is unavailable')
  const directory = await mkdtemp(join(tmpdir(), 'tiku-proxy-converter-'))
  const source = join(directory, 'trusted_proxies.txt')
  const output = join(directory, 'trusted-proxies.generated.conf')
  const original = '# unchanged\ntrusted_proxies: ["172.26.0.3", "10.0.0.0/255.0.0.0"]\n172.26.0.10-172.26.0.12\n'
  try {
    await writeFile(source, original)
    const command = `sh '${wslPath(script)}' '${wslPath(source)}' '${wslPath(output)}'`
    const result = spawnSync('bash.exe', ['-lc', command], { encoding: 'utf8', timeout: 30000 })
    assert.equal(result.status, 0, result.stderr)
    assert.equal(await readFile(source, 'utf8'), original)
    assert.match(await readFile(output, 'utf8'), /set_real_ip_from 172\.26\.0\.3;/)
    assert.match(await readFile(output, 'utf8'), /set_real_ip_from 10\.0\.0\.0\/8;/)
    assert.match(await readFile(output, 'utf8'), /set_real_ip_from 172\.26\.0\.10\/31;/)
    assert.match(await readFile(output, 'utf8'), /set_real_ip_from 172\.26\.0\.12\/32;/)
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
})

