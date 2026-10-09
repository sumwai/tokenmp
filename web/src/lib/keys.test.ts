import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError, Code } from './envelope';
import { createKey, KeysPath, listKeys, revokeKey } from './keys';

/** 密钥端点的调用形状：路径、方法、请求体与信封解包。 */

function envelopeOf(body: unknown, extra: Record<string, unknown> = {}) {
  return {
    code: 200,
    data: body,
    message: 'ok',
    page: null,
    size: null,
    total: null,
    ...extra,
  };
}

/** stubFetch 记录调用参数并返回给定信封。 */
function stubFetch(envelope: unknown) {
  const fetchMock = vi.fn().mockResolvedValue({ json: async () => envelope });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('listKeys', () => {
  it('分页与筛选进 query，分页信息取自信封', async () => {
    const fetchMock = stubFetch(
      envelopeOf({ items: [{ id: 7, name: '本地开发', key_prefix: 'tmp_abc', enabled: true, expires_at: null, last_used_at: null }] }, {
        page: 2,
        size: 20,
        total: 21,
      }),
    );

    const result = await listKeys({ page: 2, size: 20, enabled: true });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${KeysPath}?page=2&size=20&enabled=true`);
    expect(result.items).toHaveLength(1);
    expect(result.meta).toEqual({ page: 2, size: 20, total: 21 });
  });

  it('data 为 null 时返回空列表而不是抛出', async () => {
    stubFetch(envelopeOf(null));
    await expect(listKeys({ page: 1, size: 20 })).resolves.toEqual({
      items: [],
      meta: { page: null, size: null, total: null },
    });
  });

  it('业务码非 200 抛 ApiError', async () => {
    stubFetch({ code: Code.NotFound, data: null, message: '密钥不存在', page: null, size: null, total: null });
    await expect(listKeys({ page: 1, size: 20 })).rejects.toBeInstanceOf(ApiError);
  });
});

describe('createKey', () => {
  it('提交名称与过期时刻，透传一次性明文', async () => {
    const fetchMock = stubFetch(
      envelopeOf({
        id: 9,
        name: 'CI',
        key_prefix: 'tmp_xyz',
        enabled: true,
        expires_at: null,
        last_used_at: null,
        secret: 'tmp_xyz_secret',
      }),
    );

    const created = await createKey({ name: 'CI', expires_at: null });

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe(KeysPath);
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toEqual({ name: 'CI', expires_at: null });
    expect(created.secret).toBe('tmp_xyz_secret');
  });
});

describe('revokeKey', () => {
  it('POST 到吊销路径，data 为空也算成功', async () => {
    const fetchMock = stubFetch(envelopeOf(null));
    await expect(revokeKey(42)).resolves.toBeUndefined();
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${KeysPath}/42/revoke`);
  });
});
