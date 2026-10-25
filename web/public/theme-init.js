/* Run synchronously in head, before Vue. Runtime takeover re-reads these inputs. */
(function () {
  var mode = 'system'
  try {
    var saved = window.localStorage.getItem('tiku.theme-mode.v1')
    if (saved === 'light' || saved === 'dark' || saved === 'system') mode = saved
  } catch (error) { /* Storage restrictions must not block rendering. */ }
  var systemDark = false
  try { systemDark = Boolean(window.matchMedia('(prefers-color-scheme: dark)').matches) } catch (error) { /* Light fallback. */ }
  var resolved = mode === 'system' ? (systemDark ? 'dark' : 'light') : mode
  var root = document.documentElement
  root.setAttribute('data-theme-mode', mode)
  root.setAttribute('data-theme', resolved)
  root.style.colorScheme = resolved
  root.style.backgroundColor = resolved === 'dark' ? '#111113' : '#F5F5F7'
})()
