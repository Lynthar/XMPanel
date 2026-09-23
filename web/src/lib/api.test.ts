import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import api, { refreshAccessToken } from './api'
import { useAuthStore } from '@/store/auth'

// fakeBackend answers /auth/refresh with a fresh token each time and every
// other request with 401 unless it carries the latest one.
function fakeBackend() {
  const calls = { refresh: 0 }
  let current = 'token-0'
  api.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    await new Promise((r) => setTimeout(r, 5))
    const respond = (status: number, data: unknown): AxiosResponse => ({ status, statusText: '', data, headers: {}, config })
    if (config.url === '/auth/refresh') {
      calls.refresh++
      current = `token-${calls.refresh}`
      return respond(200, { access_token: current })
    }
    if (config.headers.Authorization !== `Bearer ${current}`) {
      throw new AxiosError('unauthorized', 'ERR_BAD_REQUEST', config, null, respond(401, {}))
    }
    return respond(200, { ok: true })
  }
  return calls
}

describe('refresh', () => {
  const adapter = api.defaults.adapter

  beforeEach(() => {
    useAuthStore.getState().setAccessToken('stale')
  })

  afterEach(() => {
    api.defaults.adapter = adapter
    vi.unstubAllGlobals()
  })

  it('sends one refresh for requests that expire together', async () => {
    const calls = fakeBackend()

    const results = await Promise.all([api.get('/a'), api.get('/b'), api.get('/c')])

    expect(calls.refresh).toBe(1)
    expect(results.map((r) => r.status)).toEqual([200, 200, 200])
    expect(useAuthStore.getState().accessToken).toBe('token-1')
  })

  it('holds a Web Lock across tabs while refreshing', async () => {
    const calls = fakeBackend()
    const request = vi.fn((_name: string, fn: () => Promise<unknown>) => fn())
    vi.stubGlobal('navigator', { ...navigator, locks: { request } })

    await expect(refreshAccessToken()).resolves.toBe('token-1')

    expect(request).toHaveBeenCalledWith('xmpanel-auth-refresh', expect.any(Function))
    expect(calls.refresh).toBe(1)
  })
})
