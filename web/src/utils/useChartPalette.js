import { computed, inject, onBeforeUnmount, ref } from 'vue'
import { THEME_CONTROLLER_KEY } from './themeMode.mjs'
import { readPortraitPalette } from '@/pages/UserPortrait/portraitChartOptions.mjs'

export function useChartPalette() {
  const controller = inject(THEME_CONTROLLER_KEY, null)
  const theme = ref(controller?.getState().resolved || 'light')
  const unsubscribe = controller?.subscribe(state => { theme.value = state.resolved })
  onBeforeUnmount(() => { unsubscribe?.() })
  return computed(() => {
    void theme.value
    return readPortraitPalette({
      element: typeof document === 'undefined' ? null : document.documentElement,
      readStyle: (element, property) => typeof window !== 'undefined' && typeof window.getComputedStyle === 'function' ? window.getComputedStyle(element).getPropertyValue(property) : '',
    })
  })
}
