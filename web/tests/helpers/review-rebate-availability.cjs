function reviewRebateAvailabilityResponse(config, enabled = false) {
  if (config.method === 'get' && config.url === '/api/user/review-rebate/availability') {
    return Promise.resolve({ status: 200, data: { enabled }, headers: {}, config, request: { memoryOnly: true } })
  }
}

module.exports = { reviewRebateAvailabilityResponse }
