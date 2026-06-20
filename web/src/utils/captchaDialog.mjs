import Swal from 'sweetalert2'
import {
  buildCaptchaChallengePayload,
  buildCaptchaVerifyPayload,
  buildRemoteCaptchaConfig,
  normalizeCaptchaType,
} from './captchaHelpers.mjs'

function buildCaptchaDialogHTML(imageData) {
  return `<div class="captcha-dialog-root" style="display:flex;flex-direction:column;gap:14px;text-align:left;"><div class="captcha-dialog-copy" style="font-size:14px;line-height:1.7;color:#475569;">请输入下方图片中的验证码</div><div class="captcha-dialog-panel" style="display:flex;align-items:stretch;gap:12px;padding:14px;border-radius:16px;background:linear-gradient(180deg,#f8fafc 0%,#f1f5f9 100%);border:1px solid #e2e8f0;"><div class="captcha-dialog-preview" style="flex:1;min-width:0;padding:10px;border-radius:12px;background:#ffffff;border:1px solid #dbe4ee;box-shadow:inset 0 1px 0 rgba(255,255,255,0.8);"><img id="captcha-popup-image" src="${imageData}" alt="captcha" style="display:block;width:100%;border-radius:10px;cursor:pointer;" /></div><button id="captcha-refresh-trigger" type="button" style="align-self:center;height:42px;padding:0 16px;border:none;border-radius:12px;background:#e2e8f0;color:#0f172a;font-size:13px;font-weight:600;cursor:pointer;white-space:nowrap;box-shadow:0 1px 2px rgba(15,23,42,0.08);">刷新验证码</button></div><div class="captcha-dialog-hint" id="captcha-dialog-hint" style="font-size:12px;line-height:1.6;color:#64748b;">点击验证码图片也可刷新验证码</div><input id="captcha-code-input" class="swal2-input captcha-dialog-input" placeholder="请输入图形验证码" /></div>`
}

function setCaptchaHint(message, isError = false) {
  const hint = document.getElementById('captcha-dialog-hint')
  if (!hint) {
    return
  }
  hint.textContent = message
  hint.style.color = isError ? '#dc2626' : '#64748b'
}

export async function openCaptchaDialog({
  imageData,
  refreshCaptchaChallenge,
}) {
  const result = await Swal.fire({
    title: '请完成人机验证',
    html: buildCaptchaDialogHTML(imageData),
    focusConfirm: false,
    preConfirm: () => document.getElementById('captcha-code-input')?.value || '',
    confirmButtonText: '确认',
    showCancelButton: true,
    cancelButtonText: '取消',
    didOpen: () => {
      const captchaImage = document.getElementById('captcha-popup-image')
      const captchaRefreshTrigger = document.getElementById('captcha-refresh-trigger')

      const handleRefresh = async () => {
        try {
          const nextImageData = await refreshCaptchaChallenge()
          if (captchaImage) {
            captchaImage.src = nextImageData
          }
          setCaptchaHint('点击验证码图片也可刷新验证码')
        } catch (error) {
          setCaptchaHint(error?.message || '验证码刷新失败，请重试', true)
        }
      }

      if (captchaImage) {
        captchaImage.addEventListener('click', () => {
          void handleRefresh()
        })
      }
      if (captchaRefreshTrigger) {
        captchaRefreshTrigger.addEventListener('click', () => {
          void handleRefresh()
        })
      }
    },
    heightAuto: false,
    scrollbarPadding: false,
  })

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
  mount.style.background = 'rgba(15, 23, 42, 0.45)'
  mount.style.padding = '24px'
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
}) {
  const remoteConfig = buildRemoteCaptchaConfig(baseUrl, normalizeCaptchaType(captchaType))
  await loadRemoteCaptchaSDK(remoteConfig.loaderUrl)

  return new Promise((resolve, reject) => {
    let settled = false
    let tacInstance = null
    const { mount, mountId } = createRemoteCaptchaMount()

    const settle = (callback) => {
      if (settled) {
        return
      }
      settled = true
      if (tacInstance && typeof tacInstance.destroyWindow === 'function') {
        tacInstance.destroyWindow()
      }
      removeRemoteCaptchaMount(mount)
      callback()
    }

    mount.addEventListener('click', (event) => {
      if (event.target === mount) {
        settle(() => reject(new UserCancelledCaptchaError('请先完成人机验证')))
      }
    })

    window.initTAC(remoteConfig.tacPath, {
      requestCaptchaDataUrl: remoteConfig.requestCaptchaDataUrl,
      validCaptchaUrl: remoteConfig.validCaptchaUrl,
      bindEl: `#${mountId}`,
      validSuccess: (res, c, tac) => {
        tacInstance = tac
        const captchaId = extractRemoteCaptchaId(res)
        if (!captchaId) {
          settle(() => reject(new Error('远程验证码结果无效')))
          return
        }
        settle(() => resolve(captchaId))
      },
      validFail: (res, c, tac) => {
        tacInstance = tac
        if (tac && typeof tac.reloadCaptcha === 'function') {
          tac.reloadCaptcha()
        }
      },
      btnCloseFun: (el, tac) => {
        tacInstance = tac
        settle(() => reject(new UserCancelledCaptchaError('请先完成人机验证')))
      },
    }, {
      logoUrl: null,
    }).then((tac) => {
      tacInstance = tac
      tac.init()
    }).catch((error) => {
      settle(() => {
        reject(new RemoteCaptchaUnavailableError(error?.message || '远程验证码初始化失败'))
      })
    })
  })
}

async function ensureLocalCaptchaToken({
  scene,
  targetForm,
  createCaptchaChallenge,
  verifyCaptchaChallenge,
}) {
  const requestCaptchaChallenge = async () => {
    const challengeRes = await createCaptchaChallenge(buildCaptchaChallengePayload(scene))
    if (challengeRes?.status !== 200) {
      throw new Error(challengeRes?.message || '获取图形验证码失败')
    }
    return challengeRes
  }

  let challengeRes = await requestCaptchaChallenge()
  const refreshCaptchaChallenge = async () => {
    const refreshedChallenge = await requestCaptchaChallenge()
    challengeRes = refreshedChallenge
    return refreshedChallenge.data.imageData
  }

  const captchaCode = await openCaptchaDialog({
    imageData: challengeRes.data.imageData,
    refreshCaptchaChallenge,
  })
  if (!captchaCode) {
    throw new Error('请先完成人机验证')
  }

  const verifyRes = await verifyCaptchaChallenge(buildCaptchaVerifyPayload({
    challengeId: challengeRes.data.challengeId,
    scene,
    captchaCode,
  }))
  if (verifyRes?.status !== 200) {
    throw new Error(verifyRes?.message || '图形验证码校验失败')
  }
  targetForm.captchaToken = verifyRes?.data?.captchaToken || ''
  return targetForm.captchaToken
}

export async function ensureAuthCaptchaToken({
  scene,
  targetForm,
  publicSettings,
  createCaptchaChallenge,
  verifyCaptchaChallenge,
  verifyRemoteCaptcha,
}) {
  const provider = String(publicSettings?.captchaProvider || 'remote').trim().toLowerCase()
  if (provider === 'remote') {
    try {
      const captchaId = await openRemoteCaptchaDialog({
        baseUrl: publicSettings?.captchaApiBaseUrl,
        captchaType: publicSettings?.captchaType,
      })
      const remoteRes = await verifyRemoteCaptcha({
        scene,
        captchaId,
      })
      if (remoteRes?.status !== 200) {
        throw new Error(remoteRes?.message || '远程验证码校验失败')
      }
      targetForm.captchaToken = remoteRes?.data?.captchaToken || ''
      if (!targetForm.captchaToken) {
        throw new Error('远程验证码结果无效')
      }
      return targetForm.captchaToken
    } catch (error) {
      if (error instanceof RemoteCaptchaUnavailableError) {
        return ensureLocalCaptchaToken({
          scene,
          targetForm,
          createCaptchaChallenge,
          verifyCaptchaChallenge,
        })
      }
      throw error
    }
  }

  return ensureLocalCaptchaToken({
    scene,
    targetForm,
    createCaptchaChallenge,
    verifyCaptchaChallenge,
  })
}
