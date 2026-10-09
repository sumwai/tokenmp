import { afterEach, describe, expect, it, vi } from 'vitest';

import { DEFAULT_RECENT, MAX_RECENT, fetchAccount, normalizeRecent } from './account';
import { ApiError, Code } from './envelope';

const okEnvelope = {
  code: Code.OK,
  data: { account: { id: 1, code: 'acc-1' }, buckets: [], quotas: [], recent: [] },
  message: 'ok',
  page: null,
  size: null,
  total: null,
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('normalizeRecent', () => {
  it('缺省与非法取值回落契约默认条数', () => {
    expect(normalizeRecent(null)).toBe(DEFAULT_RECENT);
    expect(normalizeRecent('')).toBe(DEFAULT_RECENT);
    expect(normalizeRecent('-5')).toBe(DEFAULT_RECENT);
    expect(normalizeRecent('abc')).toBe(DEFAULT_RECENT);
    expect(normalizeRecent('10.5')).toBe(DEFAULT_RECENT);
  });

  it('0 是合法取值，表示不取流水', () => {
    expect(normalizeRecent('0')).toBe(0);
  });

  it('取值在上限内原样保留，超上限截到上限', () => {
    expect(normalizeRecent('20')).toBe(20);
    expect(normalizeRecent('100')).toBe(MAX_RECENT);
    expect(normalizeRecent('1000')).toBe(MAX_RECENT);
  });
});

describe('fetchAccount', () => {
  it('按 recent 组请求，解包后返回摘要', async () => {
    const fetchStub = vi.fn().mockResolvedValue({ json: async () => okEnvelope });
    vi.stubGlobal('fetch', fetchStub);

    await expect(fetchAccount(0)).resolves.toEqual(okEnvelope.data);
    expect(fetchStub.mock.calls[0][0]).toBe('/api/v1/user/account?recent=0');
  });

  it('业务码非 200 时抛 ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      json: async () => ({ ...okEnvelope, code: Code.Forbidden, data: null, message: '账户尚未开通' }),
    }));

    await expect(fetchAccount(DEFAULT_RECENT)).rejects.toSatisfy(
      (err: unknown) => err instanceof ApiError && err.code === Code.Forbidden,
    );
  });
});
