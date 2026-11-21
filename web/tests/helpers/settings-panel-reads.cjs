// Read-only fixtures for the sibling panels rendered by SystemSettings and
// its private layout. Every other request remains with the test's existing
// adapter (including its explicit no-network / no-unplanned-write checks).
const bodies = {
  '/api/refund-agreement': {text:'SYNTHETIC_AGREEMENT',defaultText:'SYNTHETIC_AGREEMENT',isDefault:true},
  '/api/system/storage-status': {degraded:false},
  '/api/user/review-rebate/availability': {enabled:false},
}
function settingsPanelRead(config) {
  if (config.method !== 'get' || config.data != null || !Object.hasOwn(bodies, config.url)) return null
  return {status:200, data:structuredClone(bodies[config.url]), headers:{}, config, request:{memoryOnly:true}}
}
module.exports = {settingsPanelRead}
