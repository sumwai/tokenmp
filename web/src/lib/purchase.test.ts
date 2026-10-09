import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError, Code } from './envelope';
import {
  OrdersPath,
  ProductsPath,
  createOrder,
  listOrders,
  listProducts,
  newIdempotencyKey,
} from './purchase';
import { DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE, ordersQueryString, parseOrdersQuery } from './purchaseUrl';

afterEach(() => {
  vi.unstubAllGlobals();
});

/** envelopeOf 造一个页面信封；data 为 null 时表示失败响应。 */
function envelopeOf(code: number, data: unknown) {
  return {
    code,
    data,
    message: code === Code.OK ? 'ok' : '失败',
    page: null,
    size: null,
    total: null,
  };
}

describe('parseOrdersQuery', () => {
  it('缺省取契约默认，非法值回落', () => {
    expect(parseOrdersQuery('')).toEqual({ page: 1, size: DEFAULT_PAGE_SIZE });
    expect(parseOrdersQuery('?page=0&size=abc')).toEqual({ page: 1, size: DEFAULT_PAGE_SIZE });
    expect(parseOrdersQuery('?page=-3')).toEqual({ page: 1, size: DEFAULT_PAGE_SIZE });
  });

  it('每页条数超上限截到上限', () => {
    expect(parseOrdersQuery('?page=2&size=1000')).toEqual({ page: 2, size: MAX_PAGE_SIZE });
  });

  it('序列化后再解析得到同一条件', () => {
    const query = { page: 3, size: 50 };
    expect(parseOrdersQuery(ordersQueryString(query))).toEqual(query);
  });
});

describe('listProducts', () => {
  it('按契约路径取目录并解包 items', async () => {
    const fetchStub = vi.fn().mockResolvedValue({
      json: async () =>
        envelopeOf(Code.OK, {
          items: [{ id: 9, name: '10 元 100M token', unit: 'token', qty: '100000000', price: '10', model_scope: null, validity_days: 30 }],
        }),
    });
    vi.stubGlobal('fetch', fetchStub);

    const products = await listProducts();
    expect(fetchStub.mock.calls[0][0]).toBe(ProductsPath);
    expect(products[0].qty).toBe('100000000');
    expect(products[0].price).toBe('10');
  });

  it('业务码非 200 时抛 ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      json: async () => envelopeOf(Code.Forbidden, null),
    }));

    await expect(listProducts()).rejects.toBeInstanceOf(ApiError);
  });
});

describe('listOrders', () => {
  it('分页进 query，页码与总数取自信封', async () => {
    const fetchStub = vi.fn().mockResolvedValue({
      json: async () => ({
        ...envelopeOf(Code.OK, { items: [] }),
        page: 2,
        size: 10,
        total: 21,
      }),
    });
    vi.stubGlobal('fetch', fetchStub);

    const result = await listOrders({ page: 2, size: 10 });
    expect(fetchStub.mock.calls[0][0]).toBe(`${OrdersPath}?page=2&size=10`);
    expect(result.meta).toEqual({ page: 2, size: 10, total: 21 });
    expect(result.items).toEqual([]);
  });

  it('业务码非 200 时抛 ApiError，而不是渲染成空列表', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      json: async () => envelopeOf(Code.InternalError, null),
    }));

    await expect(listOrders({ page: 1, size: 20 })).rejects.toBeInstanceOf(ApiError);
  });
});

describe('createOrder', () => {
  it('按契约提交请求体，返回订单', async () => {
    const fetchStub = vi.fn().mockResolvedValue({
      json: async () =>
        envelopeOf(Code.OK, { id: 77, product_id: 9, qty: '2', price_paid: '20', unit_rate: '0.0000001' }),
    });
    vi.stubGlobal('fetch', fetchStub);

    const order = await createOrder({ product_id: 9, qty: '2', idempotency_key: 'order-key-0001' });
    expect(order.id).toBe(77);
    const [url, init] = fetchStub.mock.calls[0];
    expect(url).toBe(OrdersPath);
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toEqual({
      product_id: 9,
      qty: '2',
      idempotency_key: 'order-key-0001',
    });
  });

  it('商品不存在时抛带业务码的 ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      json: async () => envelopeOf(Code.NotFound, null),
    }));

    await expect(
      createOrder({ product_id: 404, qty: '1', idempotency_key: 'order-key-0001' }),
    ).rejects.toSatisfy((err: unknown) => err instanceof ApiError && err.code === Code.NotFound);
  });
});

describe('newIdempotencyKey', () => {
  it('键长落在契约的 8..64 之内，且两次取值不同', () => {
    const first = newIdempotencyKey();
    const second = newIdempotencyKey();
    expect(first.length).toBeGreaterThanOrEqual(8);
    expect(first.length).toBeLessThanOrEqual(64);
    expect(first).not.toBe(second);
  });
});
