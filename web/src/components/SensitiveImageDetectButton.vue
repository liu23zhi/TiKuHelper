<template>
  <a-button v-if="canManage" :key="surfaceVersion" size="small" type="link" :loading="busy" :disabled="busy || !validSha" :onClick="bind(detect)">重新检测</a-button>
  <span v-if="canManage && feedback" class="detect-feedback" role="status">
    <span>{{ feedback }}</span>
    <code v-if="errorCode" class="detect-diagnostic">{{ errorCode }}</code>
    <span v-if="diagnosticId" class="detect-diagnostic">诊断编号 <code data-request-diagnostic>{{ diagnosticId }}</code></span>
  </span>
</template>
<script>
import { computed, ref } from 'vue'
import Swal from 'sweetalert2'
import { manualDetectSensitiveImage } from '../api/SensitiveImageApi'
import { useCurrentUserDisplay } from '../utils/currentUser'
import { useAdminPanelLease } from '../utils/useAdminPanelLease'
import { buildRequestFailureView } from '../utils/requestFailureView.mjs'
import { getTransportRequestId } from '../utils/requestHelpers.mjs'
import { presentImageDetectionError } from '../pages/SensitiveImage/sensitiveImageHelpers.mjs'
export default {
  props: { sha: { type: String, required: true }, disabled: Boolean }, emits: ['detected'],
  setup(props, { emit }) {
    const { currentUser } = useCurrentUserDisplay()
    const busy = ref(false), feedback = ref(''), diagnosticId = ref(''), errorCode = ref('')
    const validSha = computed(() => /^[0-9a-f]{64}$/i.test(props.sha))
    const { canManage, capture, bind, surfaceVersion } = useAdminPanelLease(currentUser, {
      disabled: () => props.disabled, resource: () => props.sha,
      clear: () => { busy.value = false; feedback.value = ''; diagnosticId.value = ''; errorCode.value = '' },
    })
    async function detect() {
      if (!canManage.value || !validSha.value || busy.value) return
      const sha = props.sha, guard = capture()
      const owns = () => guard() && sha === props.sha
      busy.value = true; feedback.value = ''; diagnosticId.value = ''; errorCode.value = ''
      try {
        const confirmation = await Swal.fire({ icon: 'question', title: '重新检测此图片？', text: '将调用图片检测服务，并同步所有引用此图片的工单。', showCancelButton: true, confirmButtonText: '检测', cancelButtonText: '取消' })
        if (!confirmation.isConfirmed || !owns() || props.disabled) return
        const response = await manualDetectSensitiveImage({ imageSha256: sha })
        if (!owns()) return
        if (response?.status !== 200) {
          const view = presentImageDetectionError(response?.data)
          feedback.value = view.message
          diagnosticId.value = view.diagnosticId || getTransportRequestId(response) || ''
          errorCode.value = view.code
          return
        }
        const state = response.data?.result?.state || response.data?.state || response.data?.status
        feedback.value = response.data?.attachSyncError
          ? '检测完成，但工单图片状态同步未完成，请刷新后重试。'
          : ({ clean: '检测正常', sensitive: '检测为敏感图片', error: '检测失败，请重试', failed: '检测失败，请重试' })[state] || '检测完成，请核对结果'
        emit('detected')
      } catch (error) {
        if (owns()) {
          const view = buildRequestFailureView(error)
          feedback.value = view?.kind === 'unknown-outcome'
            ? '检测结果尚未确认，请先刷新核对，勿重复检测。'
            : '图片检测请求未完成，请稍后重试。'
          diagnosticId.value = getTransportRequestId(error) || ''
          const receipt = presentImageDetectionError(error?.response?.data)
          if (receipt.code) {
            feedback.value = receipt.message
            diagnosticId.value = receipt.diagnosticId || diagnosticId.value
            errorCode.value = receipt.code
          }
        }
      }
      finally { if (owns()) busy.value = false }
    }
    return { canManage, busy, feedback, diagnosticId, errorCode, detect, bind, surfaceVersion, validSha }
  },
}
</script>
<style scoped>.detect-feedback { display:block; font-size:12px; color:var(--console-text-secondary,#64748b); }</style>
