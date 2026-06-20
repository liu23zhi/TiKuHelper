import Swal from 'sweetalert2'

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
