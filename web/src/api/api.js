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
const responseInterceptor = (response) => {
    if (response.status !== 200) {
        console.log(response.data?.message)
    }
    return response.data;
};

async function requestParser(config) {
    try {
        const response = await executeWithRetry((attemptConfig) => xmig6Instance.request(attemptConfig), config)
        return responseInterceptor(response)
    } catch (error) {
        throw toUserRequestError(config, error)
    }
}

export async function parseFile(data) {
    return await requestParser({ url: '/parse', method: 'get', params: data, retryPolicy: 'single' })
}

export async function reParseFile(data) {
    return await requestParser({ url: '/parse', method: 'post', data: qs.stringify({html: data.html}), retryPolicy: 'single' })
}


