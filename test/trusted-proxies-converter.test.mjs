import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtemp, readFile, writeFile, rm, access } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

// Native Linux/CI contract: no bash.exe, private evidence path, or optional skip.
const root = process.env.TIKU_PROXY_SOURCE || resolve(dirname(fileURLToPath(import.meta.url)), '..')
const script = join(root, 'run/nginx/trusted-proxies-convert.sh')
async function convert(content, check) {
  const directory = await mkdtemp(join(tmpdir(), 'tiku-proxy-strict-'))
  const source = join(directory, 'trusted_proxies.txt'), output = join(directory, 'generated.conf')
  const original = content === null ? null : Buffer.from(content, 'utf8')
  try {
    if (original !== null) await writeFile(source, original)
    const result = spawnSync('sh', [script, source, output], { encoding: 'utf8', timeout: 10000, env: { ...process.env, LC_ALL: 'C' } })
    assert.ifError(result.error)
    assert.equal(result.signal, null, 'converter must complete rather than time out or receive a signal')
    const generated = await readFile(output, 'utf8')
    if (original !== null) assert.deepEqual(await readFile(source), original, 'source bytes must remain unchanged')
    else await assert.rejects(access(source), { code: 'ENOENT' })
    check(result, generated)
  } finally {
    assert.equal(dirname(resolve(directory)), resolve(tmpdir()))
    assert.ok(directory.startsWith(join(tmpdir(), 'tiku-proxy-strict-')))
    await rm(directory, { recursive: true, force: true })
  }
}
function empty(result, generated) {
  assert.equal(result.status, 0, result.stderr)
  assert.equal(generated, '', 'no implicit trust source may be emitted')
}
function restricted(expected) {
  return (result, generated) => {
    assert.equal(result.status, 0, result.stderr)
    assert.deepEqual(generated.trim().split('\n'), expected.map(address => `set_real_ip_from ${address};`), 'only the explicitly configured restricted directives are permitted; no implicit LAN defaults')
  }
}
function rejected(result, generated) {
  assert.equal(result.status, 1, 'production policy rejects invalid or whole-family trust')
  assert.match(result.stderr, /trusted-proxies:/)
  assert.equal(generated, '', 'a rejected mixture must not leave partial trust directives')
}

test('missing input emits no implicit trust and never creates the source', async () => {
  await convert(null, empty)
})
test('empty input emits no implicit trust', async () => {
  await convert('', empty)
})
test('BOM CRLF comment-only input emits no implicit trust without changing bytes', async () => {
  await convert('\uFEFF# only comments\r\n// another comment\r\n', empty)
})
test('standalone case-insensitive none explicitly disables trusted forwarding', async () => {
  await convert(' NoNe \n', empty)
})
test('explicit rich formats retain CIDR range and netmask conversion without added LAN defaults', async () => {
  await convert('# unchanged\ntrusted_proxies: ["172.26.0.3", "10.0.0.0/255.0.0.0"]\n172.26.0.10-172.26.0.12\nset_real_ip_from 172.26.0.3;\n', restricted(['172.26.0.3', '10.0.0.0/8', '172.26.0.10/31', '172.26.0.12/32']))
})
test('invalid-only input fails closed with an empty generated configuration', async () => {
  await convert('this-is-not-an-ip\n', rejected)
})
test('invalid mixtures and none mixtures reject instead of retaining partial directives', async () => {
  for (const input of ['192.0.2.3\nthis-is-not-an-ip\n', 'none\n192.0.2.3\n', 'none\nnone\n']) await convert(input, rejected)
})
test('explicit wildcards and direct whole-family prefixes are rejected by production policy', async () => {
  for (const input of ['*\n', 'all\n', '0.0.0.0/0\n', '::/0\n']) await convert(input, rejected)
})
test('the unsorted union of two IPv4 halves cannot bypass the whole-family guard', async () => {
  await convert('128.0.0.0/1\n0.0.0.0/1\n', rejected)
})
test('the unsorted union of two IPv6 halves cannot bypass the whole-family guard', async () => {
  await convert('8000::/1\n::/1\n', rejected)
})
test('mixed restricted IPv4 IPv6 and adjacent unions with a remaining hole stay allowed', async () => {
  await convert('0.0.0.0/2\n64.0.0.0/2\n128.0.0.0/2\n2001:db8::/32\nfe80::1\n', restricted(['0.0.0.0/2', '64.0.0.0/2', '128.0.0.0/2', '2001:db8::/32', 'fe80::1']))
  await convert('::/2\n4000::/2\n8000::/2\n192.0.2.3\n', restricted(['::/2', '4000::/2', '8000::/2', '192.0.2.3']))
})
test('malformed IPv6 and ambiguous leading-zero IPv4 are not accepted as restricted entries', async () => {
  for (const input of ['2001:::1\n', '2001:db8:00000::1\n', '1:2:3:4:5:6:7:8:9/128\n', '010.0.0.1\n']) await convert(input, rejected)
})
