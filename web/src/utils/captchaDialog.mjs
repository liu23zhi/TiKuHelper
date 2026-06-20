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

function loadRemoteCaptchaSDK(loaderUrl) {
  if (typeof window === 'undefined' || typeof document === 'undefined') {
    return Promise.reject(new Error('当前环境无法加载远程验证码'))
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
      reject(new Error('远程验证码 SDK 加载失败'))
    }
    script.onerror = () => {
      reject(new Error('远程验证码 SDK 加载失败'))
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

export async function openRemoteCaptchaDialog({
  baseUrl,
  captchaType,
}) {
  const remoteConfig = buildRemoteCaptchaConfig(baseUrl, normalizeCaptchaType(captchaType))
  await loadRemoteCaptchaSDK(remoteConfig.loaderUrl)

  return new Promise((resolve, reject) => {
    let settled = false
    let tacInstance = null
    Swal.fire({
      title: '请完成人机验证',
      html: '<div id="remote-captcha-box" style="min-height:320px;"></div>',
      showConfirmButton: false,
      showCancelButton: true,
      cancelButtonText: '取消',
      heightAuto: false,
      scrollbarPadding: false,
      didOpen: () => {
        window.initTAC({
          requestCaptchaDataUrl: remoteConfig.requestCaptchaDataUrl,
          validCaptchaUrl: remoteConfig.validCaptchaUrl,
          bindEl: '#remote-captcha-box',
          validSuccess: (res, c, tac) => {
            tacInstance = tac
            const captchaId = extractRemoteCaptchaId(res)
            if (!captchaId) {
              if (!settled) {
                settled = true
                reject(new Error('远程验证码结果无效'))
              }
              Swal.close()
              return
            }
            if (!settled) {
              settled = true
              resolve(captchaId)
            }
            if (tac && typeof tac.destroyWindow === 'function') {
              tac.destroyWindow()
            }
            Swal.close()
          },
          validFail: (res, c, tac) => {
            tacInstance = tac
            if (tac && typeof tac.reloadCaptcha === 'function') {
              tac.reloadCaptcha()
            }
          },
        }, {
          logoUrl: null,
        }).then((tac) => {
          tacInstance = tac
          tac.init()
        }).catch((error) => {
          if (!settled) {
            settled = true
            reject(error)
          }
          Swal.close()
        })
      },
      willClose: () => {
        if (tacInstance && typeof tacInstance.destroyWindow === 'function') {
          tacInstance.destroyWindow()
        }
        if (!settled) {
          settled = true
          reject(new Error('请先完成人机验证'))
        }
      },
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
    } catch (_) {
      return ensureLocalCaptchaToken({
        scene,
        targetForm,
        createCaptchaChallenge,
        verifyCaptchaChallenge,
      })
    }
  }

  return ensureLocalCaptchaToken({
    scene,
    targetForm,
    createCaptchaChallenge,
    verifyCaptchaChallenge,
  })
}
