import { ref } from 'vue'

import { DEFAULT_SITE_NAME, normalizeSiteName } from './siteNameHelpers.mjs'

export const siteName = ref(DEFAULT_SITE_NAME)

export function applySiteName(value) {
  const normalized = normalizeSiteName(value)
  siteName.value = normalized
  if (typeof document !== 'undefined') {
    document.title = normalized
  }
  return normalized
}
