import axios from 'axios'
import qs from 'qs';
import { executeWithRetry, toUserRequestError } from '../utils/requestRetry.mjs'

const xmig6Instance = axios.create({
    baseURL: '/sqp/api',
    timeout: 60 * 1000,
    validateStatus: function (status) {
        return status >= 200 && status < 600
    }
})
// Keep only public error classification. Axios config/body/cause can contain
// private document HTML, upload IDs or upstream connection details.
const parserFailure = error => {
    const failure = new Error('文档解析请求失败，请稍后重试。')
    failure.name = 'RequestError'
    const status = error?.response?.status
    if (Number.isInteger(status) && status >= 100 && status <= 599) {
        failure.code = 'PARSER_HTTP_ERROR'
        failure.response = { status }
    } else {
        const allowed = ['ERR_NETWORK', 'ECONNRESET', 'ECONNABORTED', 'ETIMEDOUT', 'ERR_CANCELED', 'ERR_BAD_RESPONSE', 'ERR_BAD_REQUEST']
        failure.code = allowed.includes(error?.code) ? error.code
            : /^(Network Error|Failed to fetch)$/i.test(error?.message || '') ? 'ERR_NETWORK' : 'PARSER_ERROR'
        if (error?.name === 'AbortError') {
            failure.name = 'AbortError'
            failure.code = 'ERR_CANCELED'
        }
    }
    return failure
}

const responseInterceptor = response => {
    if (response?.status !== 200) throw parserFailure({ response })
    // The page owns paper validation; preserve the successful raw body identity.
    return response.data
}

async function requestParser(config) {
    try {
        const response = await executeWithRetry(attemptConfig => xmig6Instance.request(attemptConfig), config)
        return responseInterceptor(response)
    } catch (error) {
        // Preserve existing POST unknown-outcome semantics with a safe cause,
        // never the original Axios error/config or an automatic replay.
        throw toUserRequestError(config, parserFailure(error))
    }
}

export async function parseFile(data) {
    return await requestParser({ url: '/parse', method: 'get', params: data, retryPolicy: 'single' })
}

export async function reParseFile(data) {
    return await requestParser({ url: '/parse', method: 'post', data: qs.stringify({html: data.html}), retryPolicy: 'single' })
}


