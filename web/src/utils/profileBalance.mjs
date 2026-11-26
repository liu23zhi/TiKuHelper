import { formatMoneyValue, MONEY_DECIMALS } from './moneyFormat.mjs'

// The wire fields are flat and can be numeric. Keep exact decimals intact if
// the caller already has their textual representation.
const decimalPattern = /^(-?)(\d{1,12})(?:\.(\d{1,6}))?$/
const dataMissing = '余额数据缺失'
const dataInvalid = '余额数据异常'

function readBucket(value, allowDebt) {
  if (value == null || value === '') return { text: dataMissing }
  let decimal = value
  if (typeof value === 'number' && Number.isFinite(value)) {
    decimal = String(value)
    if (!decimalPattern.test(decimal)) decimal = value.toFixed(MONEY_DECIMALS)
  }
  const match = typeof decimal === 'string' ? decimalPattern.exec(decimal) : null
  if (!match) return { text: dataInvalid }
  const magnitude = BigInt(match[2] + (match[3] || '').padEnd(MONEY_DECIMALS, '0'))
  const units = match[1] ? -magnitude : magnitude
  if (!allowDebt && (units < 0n || (typeof value === 'number' && value < 0))) return { text: dataInvalid }
  return { text: yuan(units), units }
}

function yuan(units) {
  const sign = units < 0n ? '-' : ''
  const digits = (units < 0n ? -units : units).toString().padStart(MONEY_DECIMALS + 1, '0')
  return '¥ ' + formatMoneyValue(sign + digits.slice(0, -MONEY_DECIMALS) + '.' + digits.slice(-MONEY_DECIMALS))
}

export function buildProfileBalanceDisplay(user, readState) {
  if (readState !== 'ready') {
    const state = readState === 'failed' ? '余额加载失败' : '余额加载中…'
    return { total: state, principal: state, gift: state }
  }
  const principal = readBucket(user?.balance, true)
  const gift = readBucket(user?.giftBalance, false)
  const total = principal.units != null && gift.units != null
    ? yuan(principal.units + gift.units)
    : principal.text === dataInvalid || gift.text === dataInvalid ? dataInvalid : dataMissing
  return { total, principal: principal.text, gift: gift.text }
}
