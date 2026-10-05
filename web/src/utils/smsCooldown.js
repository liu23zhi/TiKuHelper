import { computed, onBeforeUnmount, ref } from 'vue'

import {
  SMS_SEND_COOLDOWN_SECONDS,
  formatSMSCodeButtonText,
} from './smsCooldownHelpers.mjs'

export function useSmsCooldown(durationSeconds = SMS_SEND_COOLDOWN_SECONDS) {
  const normalizedDuration = Math.max(1, Math.ceil(Number(durationSeconds) || SMS_SEND_COOLDOWN_SECONDS))
  const remaining = ref(0)
  let timer = null

  const active = computed(() => remaining.value > 0)
  const buttonText = computed(() => formatSMSCodeButtonText(remaining.value))

  const stop = () => {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  const start = () => {
    stop()
    remaining.value = normalizedDuration
    timer = setInterval(() => {
      remaining.value = Math.max(0, remaining.value - 1)
      if (remaining.value === 0) {
        stop()
      }
    }, 1000)
  }

  onBeforeUnmount(stop)

  return {
    remaining,
    active,
    buttonText,
    start,
    stop,
  }
}
