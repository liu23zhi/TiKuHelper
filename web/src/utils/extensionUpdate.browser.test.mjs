import test from 'node:test'
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import path from 'node:path'
import { startExtensionUpdateFixture } from '../../scripts/extension-update-fixture.mjs'

test('extension update UI saves validated settings, refreshes once, retains failed cache and fits mobile', {
  skip: !process.env.TIKU_BROWSER_MODULE_ROOT,
  timeout: 90_000,
}, async () => {
  const require = createRequire(path.join(path.resolve(process.env.TIKU_BROWSER_MODULE_ROOT), 'package.json'))
  const puppeteer = require('puppeteer')
  const fixture = await startExtensionUpdateFixture()
  let browser
  try {
    browser = await puppeteer.launch({ headless: true, args: ['--no-sandbox', '--disable-gpu'] })
    const page = await browser.newPage(), errors = []
    page.on('pageerror', error => errors.push(error.message))
    await page.setViewport({ width: 1440, height: 1100 })
    await page.goto(fixture.url, { waitUntil: 'networkidle0' })
    await page.waitForSelector('#extension-edge-id')
    assert.equal(await page.$eval('#extension-edge-id', element => element.value), 'lcmeenlnjihhjcjoakekikoaldigonac')
    const button = async text => {
      const handle = await page.evaluateHandle(value => Array.from(document.querySelectorAll('button')).find(element => element.textContent.trim() === value), text)
      assert.ok(handle.asElement(), `button ${text} exists`)
      return handle.asElement()
    }
    assert.equal(await (await button('保存配置')).evaluate(element => element.disabled), true)
    assert.equal(await page.$eval('button[aria-label="检查 Google Chrome 更新"]', element => element.disabled), true)
    await page.$eval('#extension-interval', element => element.focus())
    await page.keyboard.down('Control'); await page.keyboard.press('A'); await page.keyboard.up('Control'); await page.keyboard.type('90'); await page.keyboard.press('Tab')
    await page.click('#extension-auto-update')
    assert.equal(await page.$eval('button[aria-label="检查 Microsoft Edge 更新"]', element => element.disabled), true, 'dirty settings cannot check an old source')
    await (await button('保存配置')).click()
    await page.waitForFunction(() => document.body.textContent.includes('扩展更新配置已保存'))
    const saves = fixture.requests.filter(request => request.method === 'PUT')
    assert.deepEqual(saves.map(request => request.body), [{ autoUpdate: false, intervalMinutes: 90, edgeId: 'lcmeenlnjihhjcjoakekikoaldigonac', chromeId: '' }])
    await page.click('button[aria-label="检查 Microsoft Edge 更新"]')
    await page.waitForFunction(() => document.querySelector('.release-details').textContent.includes('1.2.4'))
    fixture.setRefreshFailure(true)
    await page.click('button[aria-label="检查 Microsoft Edge 更新"]')
    await page.waitForFunction(() => document.body.textContent.includes('检查更新失败，已有缓存保留'))
    assert.match(await page.$eval('.release-details', element => element.textContent), /1\.2\.4/)
    assert.doesNotMatch(await page.$eval('body', element => element.textContent), /private|secret\.key/)
    assert.equal(fixture.requests.filter(request => request.url.endsWith('/refresh')).length, 2, 'each manual check sends one POST even on failure')
    await page.setViewport({ width: 390, height: 844 })
    await page.waitForFunction(() => Math.abs(document.querySelector('.layout-sider').getBoundingClientRect().width - 64) < 0.5)
    await page.screenshot({ path: process.env.TIKU_EXTENSION_SCREENSHOT || path.join(process.env.TEMP || '.', 'tiku-extension-mobile.png'), fullPage: true })
    const layout = await page.$eval('.extension-updates-page', element => ({ width: element.clientWidth, scroll: element.scrollWidth }))
    assert.ok(layout.scroll <= layout.width + 1, `mobile content must fit ${JSON.stringify(layout)}`)
    assert.deepEqual(errors, [])
  } finally {
    if (browser) await browser.close()
    await fixture.close()
  }
})
