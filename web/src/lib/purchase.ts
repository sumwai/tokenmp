import { requestPage, type PageMeta } from './client';
import { api } from './generated/api';
import type { components } from './generated/schema';
import { ordersQueryString, type OrdersQuery } from './purchaseUrl';

/**
 * 商品目录与订单端点（`/api/v1/user/products`、`/api/v1/user/orders`）的类型与调用。
 *
 * 字段类型一律取自契约生成物（web/AGENTS.md：禁止手写响应类型）；金额、数量与折算率
 * 都是十进制字符串，本层原样透传，不做任何数值转换。
 */

/** Product 是一个可购买的商品档位。 */
export type Product = components['schemas']['Product'];

/** Order 是一笔订单。 */
export type Order = components['schemas']['Order'];

/** 商品目录与订单集合的路径，与契约一致。 */
export const ProductsPath = '/api/v1/user/products';
export const OrdersPath = '/api/v1/user/orders';

/** listProducts 取商品目录；目录不分页，服务端按「一页含全部」填分页字段。 */
export async function listProducts(): Promise<Product[]> {
  const data = await api.listUserProducts();
  return data?.items ?? [];
}

/** listOrders 列出当前账户的订单；分页信息取自信封而不是条目数。 */
export async function listOrders(query: OrdersQuery): Promise<{ items: Order[]; meta: PageMeta }> {
  const { data, meta } = await requestPage<{ items: Order[] }>(
    `${OrdersPath}${ordersQueryString(query)}`,
    { method: 'GET' },
  );
  return { items: data?.items ?? [], meta };
}

/** CreateOrderInput 是下单请求体；幂等键由调用方生成。 */
export interface CreateOrderInput {
  product_id: number;
  /** 购买份数，正数的十进制字符串。 */
  qty: string;
  /** 幂等键；同一键的重复提交只产生一笔订单。 */
  idempotency_key: string;
}

/** createOrder 下单购买一个档位，返回订单；重复提交返回首次创建的订单。 */
export async function createOrder(input: CreateOrderInput): Promise<Order> {
  return api.createUserOrder({ body: input });
}

const HEX_DIGITS = '0123456789abcdef';

/**
 * newIdempotencyKey 生成一次下单的幂等键。
 *
 * 键只用于服务端在 (账户, 键) 上去重，不承载业务含义。优先用 crypto 的随机源；
 * 没有 randomUUID 的环境退回随机字节拼装，键长仍在契约的 8..64 之内。
 */
export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return `order-${crypto.randomUUID()}`;
  }
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  let hex = '';
  for (const byte of bytes) {
    hex += HEX_DIGITS[byte >> 4] + HEX_DIGITS[byte & 0x0f];
  }
  return `order-${hex}`;
}
