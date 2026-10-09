import { addDecimals, formatDecimal, parseUnsignedInt } from './decimal';
import { api } from './generated/api';
import type { components } from './generated/schema';
import { type UsageGroupBy, type UsageQuery } from './usageUrl';

/**
 * 用量聚合端点（`/api/v1/user/usage/stats`）的类型、调用与展示口径。
 *
 * 响应类型取契约生成物（web/AGENTS.md：响应类型不手写）；合计与明细同源 ——
 * 同一组过滤条件下明细逐行相加等于本端点的合计（docs/compatibility.md 的「聚合读取的口径」），
 * 页面因此只展示合计，单条调用的明细走用量流水端点。
 */

/** 一个分组的用量合计。 */
export type UsageStatsItem = components['schemas']['UsageStatsItem'];

/** 一次调用的 token 用量分量。 */
export type UsageTokens = components['schemas']['UsageTokens'];

/** 用量聚合端点路径，与契约一致。 */
export const UsageStatsPath = '/api/v1/user/usage/stats';

/** 分组维度文案。 */
export const USAGE_GROUP_LABELS: Record<UsageGroupBy, string> = {
  day: '按天',
  model: '按模型',
  api_key: '按密钥',
};

/** 维度列的表头：同一个分组值在不同维度下是日期、模型名或密钥 id。 */
export const USAGE_GROUP_KEY_LABELS: Record<UsageGroupBy, string> = {
  day: '日期',
  model: '模型',
  api_key: '密钥 id',
};

/** token 分量的字段顺序：合计逐项相加时按它与契约字段对齐。 */
export const TOKEN_FIELDS: (keyof UsageTokens)[] = [
  'input_tokens',
  'output_tokens',
  'cache_read_tokens',
  'cache_write_tokens',
  'cache_write_5m_tokens',
  'cache_write_1h_tokens',
  'reasoning_tokens',
  'server_tool_uses',
];

/** TokenColumn 是表格里的一个 token 分量列。 */
export interface TokenColumn {
  /** 列标识，同时用作 React key。 */
  id: string;
  label: string;
  /** 断点以下隐藏该列（web/AGENTS.md：窄屏次要列由列定义声明）。 */
  hideBelow?: 'md' | 'lg';
  /** value 从一个分量里取该列的取值。 */
  value: (tokens: UsageTokens) => number;
}

/**
 * TOKEN_COLUMNS 是表格的 token 分量列。
 *
 * 单列直取契约字段；缓存写把不分档与两个分档字段合并 —— 契约声明三者互斥使用，
 * 同一行至多一种有取值（两套同时非零时计费侧以分档为准），因此合并得到的是
 * 「缓存写 token 总数」而不是重复计数。输入、输出是主计数，缓存与推理是其子项
 * （契约 UsageTokens 的说明），所以子项只在宽屏展开。
 */
export const TOKEN_COLUMNS: TokenColumn[] = [
  { id: 'input_tokens', label: '输入', hideBelow: 'md', value: (t) => t.input_tokens },
  { id: 'output_tokens', label: '输出', hideBelow: 'md', value: (t) => t.output_tokens },
  { id: 'cache_read', label: '缓存读', hideBelow: 'lg', value: (t) => t.cache_read_tokens },
  {
    id: 'cache_write',
    label: '缓存写',
    hideBelow: 'lg',
    value: (t) => t.cache_write_tokens + t.cache_write_5m_tokens + t.cache_write_1h_tokens,
  },
  { id: 'reasoning', label: '推理', hideBelow: 'lg', value: (t) => t.reasoning_tokens },
  { id: 'server_tool_uses', label: '服务端工具', hideBelow: 'lg', value: (t) => t.server_tool_uses },
];

/** UsageTotal 是页面的合计行：次数、各 token 分量与应扣量。 */
export interface UsageTotal {
  calls: number;
  tokens: UsageTokens;
  /** 应扣量合计；任一行不是十进制形态时为 null，页面显示占位符。 */
  chargedAmount: string | null;
}

/** emptyTokens 是全零分量，作为合计的起点。 */
function emptyTokens(): UsageTokens {
  return {
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    cache_write_5m_tokens: 0,
    cache_write_1h_tokens: 0,
    reasoning_tokens: 0,
    server_tool_uses: 0,
  };
}

/**
 * totalUsage 把分组行逐行相加得到页面合计。
 *
 * 口径与端点的合计一致，所以它在页面上与「同一区间下明细逐行相加」是同一个值：
 * 次数与 token 分量是整数，直接累加；应扣量是十进制串，按字符串相加（不经浮点）。
 */
export function totalUsage(items: UsageStatsItem[]): UsageTotal {
  const tokens = emptyTokens();
  let calls = 0;
  const amounts: string[] = [];
  for (const item of items) {
    calls += item.calls;
    for (const field of TOKEN_FIELDS) {
      tokens[field] += item.usage[field];
    }
    amounts.push(item.charged_amount);
  }
  return { calls, tokens, chargedAmount: addDecimals(amounts) };
}

/** usageStats 拉取当前账户的用量聚合；端点不分页，一次返回全部维度分组。 */
export async function usageStats(query: UsageQuery): Promise<UsageStatsItem[]> {
  const apiKeyID = query.apiKeyID === '' ? undefined : parseUnsignedInt(query.apiKeyID);
  const data = await api.getUserUsageStats({
    params: {
      query: {
        // 空串表示不过滤：不写进请求，避免把「不限」表达成空参数。
        since: query.since === '' ? undefined : query.since,
        until: query.until === '' ? undefined : query.until,
        group_by: query.groupBy,
        model: query.model === '' ? undefined : query.model,
        api_key_id: apiKeyID ?? undefined,
      },
    },
  });
  return data?.items ?? [];
}

/** formatCount 展示整数计数（调用次数与 token 分量）：千分位分隔。 */
export function formatCount(value: number): string {
  if (!Number.isFinite(value)) {
    return '—';
  }
  return String(value).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

/**
 * formatChargedAmount 展示应扣量。
 *
 * 契约里的 `charged_amount` 不带结算单位，所以只加千分位、不补单位口径的小数位
 * （按 currency 补两位会把按 token 计费的行显示错）；合计取不回时给占位符。
 */
export function formatChargedAmount(raw: string | null): string {
  return raw === null ? '—' : formatDecimal(raw, 0);
}
