// Read-only fixtures for the sibling panels rendered by SystemSettings and
// its private layout. Every other request remains with the test's existing
// adapter (including its explicit no-network / no-unplanned-write checks).
const bodies = {
  '/api/refund-agreement': {text:'SYNTHETIC_AGREEMENT',defaultText:'SYNTHETIC_AGREEMENT',isDefault:true},
  '/api/system/storage-status': {degraded:false},
  '/api/admin/referral-settings': {enabled:false,rate:0},
  '/api/tickets/config': {enabled:false,configured:false,mentionedList:[]},
  '/api/admin/ticket-image-cache/settings': {mode:'both',maxAgeDays:180,maxTotalGiB:20,cleanupIntervalMinutes:60},
  '/api/admin/log-settings': {settings:{enabled:true,maxAgeDays:30,cleanupIntervalMinutes:60}},
  '/api/user/review-rebate/availability': {enabled:false},
}
function settingsPanelRead(config) {
  if (config.method !== 'get' || config.data != null || !Object.hasOwn(bodies, config.url)) return null
  return {status:200, data:structuredClone(bodies[config.url]), headers:{}, config, request:{memoryOnly:true}}
}
module.exports = {settingsPanelRead}
