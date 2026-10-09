import { afterEach, describe, expect, it, vi } from 'vitest';

import { apiRequest, request } from './client';
import { ApiError, Code } from './envelope';
import { api } from './generated/api';
import { accessToken, clearSession, saveSession } from './session';

/** 请求层的信封解包、错误分支与参数装配；fetch 用桩替换，不经网络。 */

const ok = (data: unknown) => ({
  code: 200,
  data,
  message: 'ok',
  page: null,
  size: null,
  total: null,
});

const failure = (code: number, message: string) => ({
  code,
  data: null,
  message,
  page: null,
  size: null,
  total: null,
});

/** stubFetch 用桩替换 fetch，handler 返回该次响应解析出的 JSON。 */
function stubFetch(handler: (url: string, init: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => ({
    json: async () => handler(url, init),
  }));
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  clearSession();
});

describe('apiRequest', () => {
  it('业务码 200 时返回解包后的 data', async () => {
    stubFetch(() => ok({ id: 1, username: 'demo', role: 'member', identities: ['password'] }));
    await expect(api.getAuthSession()).resolves.toEqual({
      id: 1,
      username: 'demo',
      role: 'member',
      identities: ['password'],
    });
  });

  it('业务码非 200 时抛 ApiError 并携带 code', async () => {
    stubFetch(() => failure(Code.Conflict, '邮箱或用户名已被使用'));
    await expect(
      api.signup({
        body: { email: 'a@b.test', username: 'demo', password: 'cipher', fingerprint: 'fp-0001' },
        skipRefresh: true,
      }),
    ).rejects.toSatisfy((err: unknown) => err instanceof ApiError && err.code === Code.Conflict);
  });

  it('响应不是 JSON（如反代错误页）时按 500 处理', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        json: async () => {
          throw new Error('not json');
        },
      }),
    );
    await expect(apiRequest('/api/v1/auth/session', 'get')).rejects.toSatisfy(
      (err: unknown) => err instanceof ApiError && err.code === Code.InternalError,
    );
  });

  it('429 时把 Retry-After 秒数带进 ApiError', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        headers: new Headers({ 'Retry-After': '30' }),
        json: async () => ({
          code: Code.TooManyRequests,
          data: null,
          message: '请求过于频繁',
          page: null,
          size: null,
          total: null,
        }),
      }),
    );
    await expect(request('/api/v1/user/account?recent=10')).rejects.toSatisfy(
      (err: unknown) =>
        err instanceof ApiError && err.code === Code.TooManyRequests && err.retryAfter === 30,
    );
  });

  it('Retry-After 不是秒数（HTTP-date）或缺失时为 null', async () => {
    const cases: (Headers | undefined)[] = [
      new Headers({ 'Retry-After': 'Wed, 21 Oct 2026 07:28:00 GMT' }),
      undefined,
    ];
    for (const headers of cases) {
      vi.stubGlobal(
        'fetch',
        vi.fn().mockResolvedValue({
          headers,
          json: async () => ({
            code: Code.TooManyRequests,
            data: null,
            message: '请求过于频繁',
            page: null,
            size: null,
            total: null,
          }),
        }),
      );
      await expect(request('/api/v1/user/account?recent=10')).rejects.toSatisfy(
        (err: unknown) => err instanceof ApiError && err.retryAfter === null,
      );
    }
  });

  it('按契约装配地址：路径参数转义、查询参数按编码后的键值对追加', async () => {
    const fetchMock = stubFetch(() => ok(null));
    await api.getUserRequest({ params: { path: { request_id: 'a/b' } } });
    await api.listUserKeys({ params: { query: { page: 2, enabled: false } } });
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/user/requests/a%2Fb',
      '/api/v1/user/keys?page=2&enabled=false',
    ]);
  });

  it('业务码 401 时用刷新令牌换发一次并重放原请求', async () => {
    saveSession('stale-access', 'refresh-token');
    const seen: string[] = [];
    stubFetch((url, init) => {
      seen.push(`${url} ${new Headers(init.headers).get('Authorization')}`);
      if (url === '/api/v1/auth/refresh') {
        return ok({ access_token: 'fresh-access', expires_in: 900 });
      }
      return seen.filter((line) => line.startsWith('/api/v1/auth/session')).length === 1
        ? failure(Code.Unauthorized, '登录状态已失效')
        : ok({ id: 1, username: 'demo', role: 'member', identities: [] });
    });

    await expect(api.getAuthSession()).resolves.toMatchObject({ username: 'demo' });
    expect(seen).toEqual([
      '/api/v1/auth/session Bearer stale-access',
      '/api/v1/auth/refresh null',
      '/api/v1/auth/session Bearer fresh-access',
    ]);
    expect(accessToken()).toBe('fresh-access');
  });

  it('刷新失败时清会话并抛出原来的 401', async () => {
    saveSession('stale-access', 'bad-refresh');
    stubFetch((url) =>
      url === '/api/v1/auth/refresh'
        ? failure(Code.Unauthorized, '刷新令牌无效')
        : failure(Code.Unauthorized, '登录状态已失效'),
    );

    await expect(api.getAuthSession()).rejects.toSatisfy(
      (err: unknown) => err instanceof ApiError && err.code === Code.Unauthorized,
    );
    expect(accessToken()).toBeNull();
  });

  it('免登录端点跳过 401 重放', async () => {
    saveSession('stale-access', 'refresh-token');
    const fetchMock = stubFetch(() => failure(Code.Unauthorized, '账号或密码错误'));

    await expect(
      api.signin({
        body: { username: 'demo', password: 'cipher', fingerprint: 'fp-0001' },
        skipRefresh: true,
      }),
    ).rejects.toSatisfy((err: unknown) => err instanceof ApiError && err.code === Code.Unauthorized);
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(['/api/v1/auth/signin']);
  });
});
