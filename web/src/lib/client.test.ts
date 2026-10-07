import { afterEach, describe, expect, it, vi } from 'vitest';

import { request } from './client';
import { ApiError, Code } from './envelope';

/** 请求层的信封解包与错误分支；fetch 用桩替换，不经网络。 */

const okEnvelope = {
  code: 200,
  data: { hello: 'world' },
  message: 'ok',
  page: null,
  size: null,
  total: null,
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('request', () => {
  it('业务码 200 时返回解包后的 data', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      json: async () => okEnvelope,
    }));
    await expect(request<{ hello: string }>('/api/v1/auth/session')).resolves.toEqual({
      hello: 'world',
    });
  });

  it('业务码非 200 时抛 ApiError 并携带 code', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      json: async () => ({
        code: Code.Conflict,
        data: null,
        message: '邮箱或用户名已被使用',
        page: null,
        size: null,
        total: null,
      }),
    }));
    await expect(request('/api/v1/auth/signup', { method: 'POST', body: '{}' })).rejects.toSatisfy(
      (err: unknown) => err instanceof ApiError && err.code === Code.Conflict,
    );
  });

  it('响应不是 JSON（如反代错误页）时按 500 处理', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      json: async () => {
        throw new Error('not json');
      },
    }));
    await expect(request('/api/v1/auth/session')).rejects.toSatisfy(
      (err: unknown) => err instanceof ApiError && err.code === Code.Internal,
    );
  });
});
