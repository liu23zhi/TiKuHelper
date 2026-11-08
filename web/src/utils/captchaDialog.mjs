import Swal from 'sweetalert2'
import {
  buildCaptchaChallengePayload,
  buildCaptchaVerifyPayload,
  buildRemoteCaptchaConfig,
  normalizeCaptchaType,
} from './captchaHelpers.mjs'

// Optional page ownership. Default callers preserve their existing behaviour.
function cancelledCaptcha() {
  const error = new Error('验证码操作已退役，请在当前页面重新操作')
  error.name = 'AbortError'
  error.code = 'ERR_CANCELED'
  return error
}

function captchaGuard({ signal, isCurrent } = {}) {
  const current = () => !signal?.aborted && (typeof isCurrent !== 'function' || isCurrent())
  const assert = () => { if (!current()) throw cancelledCaptcha() }
  const wait = (promise, onAbort) => {
    return new Promise((resolve, reject) => {
      let settled = false
      const cleanup = () => signal?.removeEventListener('abort', abort)
      const finish = (fn, value) => { if (settled) return; settled = true; cleanup(); fn(value) }
      const abort = () => {
        if (settled) return
        try { onAbort?.() } catch {
          const error = new Error('验证码窗口关闭失败，请在当前页面核对')
          error.code = 'CAPTCHA_CLEANUP_FAILED'
          finish(reject, error)
          return
        }
        finish(reject, cancelledCaptcha())
      }
      signal?.addEventListener('abort', abort, { once: true })
      Promise.resolve(promise).then(value => {
        if (!current()) { abort(); return }
        finish(resolve, value)
      }, error => {
        if (!current()) { abort(); return }
        finish(reject, error)
      })
      if (!current()) abort()
    })
  }
  return { current, assert, wait, run: start => { assert(); return wait(Promise.resolve().then(() => { assert(); return start() })) } }
}

// CAPTCHA-only styles are loaded before either body-mounted dialog is created.
// A rejected load is not cached forever: the next attempt may retry the chunk.
let captchaGeometryStylesPromise = null

function ensureCaptchaGeometryStyles() {
  if (!captchaGeometryStylesPromise) {
    captchaGeometryStylesPromise = import(/* webpackChunkName: "captcha-geometry" */ '../styles/captcha-geometry-tokens.css')
      .catch((error) => {
        captchaGeometryStylesPromise = null
        throw error
      })
  }
  return captchaGeometryStylesPromise
}

function buildCaptchaDialogHTML(imageData) {
  return `<div class="captcha-dialog-root" style="display:flex;flex-direction:column;gap:var(--captcha-dialog-root-gap);text-align:left;"><div class="captcha-dialog-copy" style="font-size:14px;line-height:1.7;color:var(--text-muted);">请输入下方图片中的验证码</div><div class="captcha-dialog-panel" style="display:flex;align-items:stretch;gap:var(--captcha-dialog-panel-gap);padding:var(--captcha-dialog-panel-padding);border-radius:var(--captcha-dialog-panel-radius);background:var(--surface-subtle);border:1px solid var(--border);"><div class="captcha-dialog-preview" style="flex:1;min-width:0;padding:var(--captcha-dialog-preview-padding);border-radius:var(--captcha-dialog-preview-radius);background:var(--surface);border:1px solid var(--control-border);box-shadow:var(--captcha-dialog-preview-shadow-geometry) var(--shadow-color);"><img id="captcha-popup-image" src="${imageData}" alt="captcha" style="display:block;width:100%;border-radius:var(--captcha-dialog-image-radius);cursor:pointer;" /></div><button id="captcha-refresh-trigger" type="button" style="align-self:center;height:42px;padding:var(--captcha-dialog-refresh-padding);border:none;border-radius:var(--captcha-dialog-refresh-radius);background:var(--surface-subtle);color:var(--text);font-size:13px;font-weight:600;cursor:pointer;white-space:nowrap;box-shadow:var(--captcha-dialog-refresh-shadow-geometry) var(--shadow-color);">刷新验证码</button></div><div class="captcha-dialog-hint" id="captcha-dialog-hint" style="font-size:12px;line-height:1.6;color:var(--text-muted);">点击验证码图片也可刷新验证码</div><input id="captcha-code-input" class="swal2-input captcha-dialog-input" placeholder="请输入图形验证码" /></div>`
}

function setCaptchaHint(message, isError = false, root = document) {
  const hint = root.querySelector('#captcha-dialog-hint')
  if (!hint) {
    return
  }
  hint.textContent = message
  hint.style.color = isError ? 'var(--danger-fg)' : 'var(--text-muted)'
}

export async function openCaptchaDialog({
  imageData,
  refreshCaptchaChallenge,
  signal,
  isCurrent,
}) {
  const guard = captchaGuard({ signal, isCurrent })
  await guard.run(ensureCaptchaGeometryStyles)
  guard.assert()
  let ownedPopup = null
  const ownsPopup = () => guard.current() && ownedPopup && Swal.getPopup() === ownedPopup
  const closeOwned = () => { if (ownedPopup && Swal.getPopup() === ownedPopup) Swal.close() }
  const resultPromise = Swal.fire({
    title: '请完成人机验证',
    html: buildCaptchaDialogHTML(imageData),
    focusConfirm: false,
    ...(signal || isCurrent ? { returnFocus: false } : {}),
    preConfirm: () => ownsPopup() ? ownedPopup.querySelector('#captcha-code-input')?.value || '' : '',
    confirmButtonText: '确认',
    showCancelButton: true,
    cancelButtonText: '取消',
    didOpen: popup => {
      ownedPopup = popup
      if (!ownsPopup()) return
      const captchaImage = popup.querySelector('#captcha-popup-image')
      const captchaRefreshTrigger = popup.querySelector('#captcha-refresh-trigger')
      const handleRefresh = async () => {
        if (!ownsPopup()) return
        try {
          const nextImageData = await guard.run(refreshCaptchaChallenge)
          if (!ownsPopup()) return
          if (captchaImage) captchaImage.src = nextImageData
          setCaptchaHint('点击验证码图片也可刷新验证码', false, popup)
        } catch (error) {
          if (ownsPopup()) setCaptchaHint('验证码刷新失败，请重试', true, popup)
        }
      }
      if (captchaImage) captchaImage.addEventListener('click', () => { void handleRefresh() })
      if (captchaRefreshTrigger) captchaRefreshTrigger.addEventListener('click', () => { void handleRefresh() })
    },
    heightAuto: false,
    scrollbarPadding: false,
  })
  ownedPopup = Swal.getPopup()
  const result = await guard.wait(resultPromise, closeOwned)
  guard.assert()
  return String(result?.value || '').trim()
}

let remoteCaptchaLoaderPromise = null

class RemoteCaptchaUnavailableError extends Error {
  constructor(message) {
    super(message)
    this.name = 'RemoteCaptchaUnavailableError'
  }
}

class UserCancelledCaptchaError extends Error {
  constructor(message) {
    super(message)
    this.name = 'UserCancelledCaptchaError'
  }
}

function loadRemoteCaptchaSDK(loaderUrl) {
  if (typeof window === 'undefined' || typeof document === 'undefined') {
    return Promise.reject(new RemoteCaptchaUnavailableError('当前环境无法加载远程验证码'))
  }
  if (window.initTAC) {
    return Promise.resolve()
  }
  if (remoteCaptchaLoaderPromise) {
    return remoteCaptchaLoaderPromise
  }
  remoteCaptchaLoaderPromise = new Promise((resolve, reject) => {
    const script = document.createElement('script')
    script.src = loaderUrl
    script.async = true
    script.onload = () => {
      if (window.initTAC) {
        resolve()
        return
      }
      reject(new RemoteCaptchaUnavailableError('远程验证码 SDK 加载失败'))
    }
    script.onerror = () => {
      reject(new RemoteCaptchaUnavailableError('远程验证码 SDK 加载失败'))
    }
    document.head.appendChild(script)
  }).catch((error) => {
    remoteCaptchaLoaderPromise = null
    throw error
  })
  return remoteCaptchaLoaderPromise
}

function extractRemoteCaptchaId(res) {
  const data = res?.data || res || {}
  return String(data.id || data.captchaId || data.captcha_id || '').trim()
}

function createRemoteCaptchaMount() {
  const mount = document.createElement('div')
  const mountId = `remote-captcha-box-${Date.now()}-${Math.random().toString(16).slice(2)}`
  mount.id = mountId
  mount.style.position = 'fixed'
  mount.style.inset = '0'
  mount.style.zIndex = '10020'
  mount.style.display = 'flex'
  mount.style.alignItems = 'center'
  mount.style.justifyContent = 'center'
  mount.style.background = 'var(--overlay)'
  mount.style.padding = 'var(--captcha-remote-mount-padding)'
  document.body.appendChild(mount)
  return { mount, mountId }
}

function removeRemoteCaptchaMount(mount) {
  if (mount && mount.parentNode) {
    mount.parentNode.removeChild(mount)
  }
}

export async function openRemoteCaptchaDialog({
  baseUrl,
  captchaType,
  signal,
  isCurrent,
}) {
  const guard = captchaGuard({ signal, isCurrent })
  guard.assert()
  const remoteConfig = buildRemoteCaptchaConfig(baseUrl, normalizeCaptchaType(captchaType))
  await guard.run(ensureCaptchaGeometryStyles)
  await guard.run(() => loadRemoteCaptchaSDK(remoteConfig.loaderUrl))
  guard.assert()
  return new Promise((resolve, reject) => {
    let settled = false
    let tacInstance = null
    const { mount, mountId } = createRemoteCaptchaMount()
    const destroyOwned = () => {
      try { tacInstance?.destroyWindow?.() } finally { removeRemoteCaptchaMount(mount) }
    }
    const settle = callback => {
      if (settled) return
      settled = true
      signal?.removeEventListener('abort', abort)
      try { destroyOwned() } catch {
        reject(new Error('远程验证码窗口关闭失败，请核对当前页面'))
        return
      }
      callback()
    }
    const abort = () => settle(() => reject(cancelledCaptcha()))
    signal?.addEventListener('abort', abort, { once: true })
    if (!guard.current()) { abort(); return }
    mount.addEventListener('click', event => {
      if (!guard.current()) { abort(); return }
      if (event.target === mount) settle(() => reject(new UserCancelledCaptchaError('请先完成人机验证')))
    })
    let initialized
    try {
      initialized = window.initTAC(remoteConfig.tacPath, {
        requestCaptchaDataUrl: remoteConfig.requestCaptchaDataUrl,
        validCaptchaUrl: remoteConfig.validCaptchaUrl,
        bindEl: `#${mountId}`,
        validSuccess: (res, c, tac) => {
          if (settled) return
          tacInstance = tac
          if (!guard.current()) { abort(); return }
          const captchaId = extractRemoteCaptchaId(res)
          if (!captchaId) { settle(() => reject(new Error('远程验证码结果无效'))); return }
          settle(() => resolve(captchaId))
        },
        validFail: (res, c, tac) => {
          if (settled) return
          tacInstance = tac
          if (!guard.current()) { abort(); return }
          tac?.reloadCaptcha?.()
        },
        btnCloseFun: (el, tac) => {
          if (settled) return
          tacInstance = tac
          if (!guard.current()) { abort(); return }
          settle(() => reject(new UserCancelledCaptchaError('请先完成人机验证')))
        },
      }, { logoUrl: null })
    } catch {
      settle(() => reject(guard.current() ? new RemoteCaptchaUnavailableError('远程验证码初始化失败') : cancelledCaptcha()))
      return
    }
    Promise.resolve(initialized).then(tac => {
      if (settled || !guard.current()) {
        tac?.destroyWindow?.()
        if (!settled) abort()
        return
      }
      tacInstance = tac
      tac.init()
    }).catch(() => {
      if (settled) return
      settle(() => reject(guard.current() ? new RemoteCaptchaUnavailableError('远程验证码初始化失败') : cancelledCaptcha()))
    })
  })
}

async function ensureLocalCaptchaToken({
  scene, targetForm, createCaptchaChallenge, verifyCaptchaChallenge, signal, isCurrent,
}) {
  const guard = captchaGuard({ signal, isCurrent })
  const requestCaptchaChallenge = async () => {
    const challengeRes = await guard.run(() => createCaptchaChallenge(buildCaptchaChallengePayload(scene)))
    if (challengeRes?.status !== 200) throw new Error('获取图形验证码失败')
    return challengeRes
  }
  let challengeRes = await requestCaptchaChallenge()
  const refreshCaptchaChallenge = async () => {
    const refreshedChallenge = await requestCaptchaChallenge()
    guard.assert()
    challengeRes = refreshedChallenge
    return refreshedChallenge.data.imageData
  }
  const captchaCode = await openCaptchaDialog({
    imageData: challengeRes.data.imageData, refreshCaptchaChallenge, signal, isCurrent,
  })
  guard.assert()
  if (!captchaCode) throw new Error('请先完成人机验证')
  const verifyRes = await guard.run(() => verifyCaptchaChallenge(buildCaptchaVerifyPayload({
    challengeId: challengeRes.data.challengeId, scene, captchaCode,
  })))
  if (verifyRes?.status !== 200) throw new Error('图形验证码校验失败')
  guard.assert()
  const token = verifyRes?.data?.captchaToken
  if (typeof token !== 'string' || !token) throw new Error('图形验证码结果无效')
  targetForm.captchaToken = token
  return token
}

export async function ensureAuthCaptchaToken({
  scene, targetForm, publicSettings, createCaptchaChallenge,
  verifyCaptchaChallenge, verifyRemoteCaptcha, signal, isCurrent,
}) {
  const guard = captchaGuard({ signal, isCurrent })
  guard.assert()
  const provider = String(publicSettings?.captchaProvider || 'remote').trim().toLowerCase()
  if (provider === 'remote') {
    try {
      const captchaId = await openRemoteCaptchaDialog({
        baseUrl: publicSettings?.captchaApiBaseUrl, captchaType: publicSettings?.captchaType, signal, isCurrent,
      })
      guard.assert()
      const remoteRes = await guard.run(() => verifyRemoteCaptcha({ scene, captchaId }))
      if (remoteRes?.status !== 200) throw new Error('远程验证码校验失败')
      guard.assert()
      const token = remoteRes?.data?.captchaToken
      if (typeof token !== 'string' || !token) throw new Error('远程验证码结果无效')
      targetForm.captchaToken = token
      return token
    } catch (error) {
      guard.assert()
      if (error instanceof RemoteCaptchaUnavailableError) {
        return ensureLocalCaptchaToken({ scene, targetForm, createCaptchaChallenge, verifyCaptchaChallenge, signal, isCurrent })
      }
      throw error
    }
  }
  return ensureLocalCaptchaToken({ scene, targetForm, createCaptchaChallenge, verifyCaptchaChallenge, signal, isCurrent })
}
