<template>
  <a-input
    :id="formItemId"
    :value="displayValue"
    :placeholder="placeholder"
    :size="size"
    :disabled="disabled"
    :allow-clear="allowClear"
    addon-before="+86"
    inputmode="numeric"
    autocomplete="tel-national"
    :maxlength="11"
    @input="handleInput"
    @paste="handlePaste"
    @blur="handleBlur"
  >
    <template #prefix>
      <MobileOutlined style="color: rgba(0, 0, 0, 0.25)" />
    </template>
  </a-input>
</template>

<script>
import { computed, defineComponent, nextTick, ref, watch } from 'vue'
import { Form, message } from 'ant-design-vue'
import { MobileOutlined } from '@ant-design/icons-vue'
import { normalizePhoneInput, normalizePhonePaste } from '../utils/captchaHelpers.mjs'

export default defineComponent({
  name: 'MainlandPhoneInput',
  components: { MobileOutlined },
  props: {
    value: { type: [String, Number], default: '' },
    placeholder: { type: String, default: '请输入11位手机号，无需输入+86' },
    size: { type: String, default: undefined },
    disabled: { type: Boolean, default: false },
    allowClear: { type: Boolean, default: true },
  },
  emits: ['update:value'],
  setup(props, { emit }) {
    // 由包装控件统一通知，等待父级模型更新后再校验。
    const formItemContext = Form.useInjectFormItemContext()
    const lastValidValue = ref('')
    let changeNotificationPending = false

    const notifyFieldChange = () => {
      if (changeNotificationPending) return
      changeNotificationPending = true
      nextTick(() => {
        changeNotificationPending = false
        formItemContext.onFieldChange()
      })
    }

    const normalizeLegacyValue = (value) => {
      const text = String(value ?? '')
      const pasted = normalizePhonePaste(text)
      if (pasted) return pasted
      return /^\d{0,11}$/.test(text) ? text : null
    }

    watch(() => props.value, (value) => {
      const normalized = normalizeLegacyValue(value)
      if (normalized !== null && normalized !== String(value ?? '')) {
        emit('update:value', normalized)
      }
      if (normalized !== null) {
        lastValidValue.value = normalized
      } else {
        lastValidValue.value = ''
      }
    }, { immediate: true })

    const displayValue = computed(() => {
      const normalized = normalizeLegacyValue(props.value)
      return normalized ?? ''
    })

    const restoreInput = (event) => {
      if (event?.target) event.target.value = lastValidValue.value
    }

    const handleInput = (event) => {
      const normalized = normalizePhoneInput(event?.target?.value)
      if (normalized === '' && event?.target?.value !== '') {
        restoreInput(event)
        message.warning('请输入11位数字手机号，不要输入+86')
        return
      }
      lastValidValue.value = normalized
      emit('update:value', normalized)
      notifyFieldChange()
    }

    const handlePaste = (event) => {
      const pasted = normalizePhonePaste(event?.clipboardData?.getData('text') || '')
      event.preventDefault()
      if (!pasted) {
        restoreInput(event)
        message.warning('请粘贴11位手机号，或带+86前缀的完整手机号')
        return
      }
      lastValidValue.value = pasted
      emit('update:value', pasted)
      notifyFieldChange()
      if (event?.target) event.target.value = pasted
    }

    const handleBlur = () => formItemContext.onFieldBlur()

    return { displayValue, formItemId: formItemContext.id, handleInput, handlePaste, handleBlur }
  },
})
</script>
