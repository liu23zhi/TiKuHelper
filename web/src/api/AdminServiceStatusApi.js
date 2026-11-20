import request from '../utils/request'

/** Detailed minute-level service diagnostics. The server restricts this to admins. */
export function getAdminServiceStatus() {
  return request({
    url: '/api/admin/service-status',
    method: 'get',
    retryPolicy: 'single',
  })
}
