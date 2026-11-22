import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

test('certificate password settings use metadata reads and write-only password candidates', async () => {
  const component = await readFile(path.join(root, 'pages/SystemSettings/CertificatePasswordPanel.vue'), 'utf8')
  const api = await readFile(path.join(root, 'api/TikuComponentApi.js'), 'utf8')

  assert.match(component, /getGatewayCertificatePasswordState/)
  assert.match(component, /updateGatewayCertificatePasswords\(passwords/)
  assert.match(component, /draft\.value = ''/)
  assert.match(component, /历史密码不会回显/)
  assert.match(api, /\/api\/admin\/system-settings\/certificate-passwords/)
  assert.doesNotMatch(component, /response\.data\?\.passwords/)
})
