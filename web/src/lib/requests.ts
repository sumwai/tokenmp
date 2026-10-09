import { request, requestEnvelope } from './client';
import { ApiError, Code } from './envelope';

/**
 * 请求记录的数据层：端点调用、URL 查询参数与展示口径。
 *
 * 契约类型暂按仓库现状手写，与 docs/openapi-web.yaml 的 UsageTokens / RequestItem /
 * RequestAttempt / RequestDetail / RequestStatsItem 逐字段对齐；#135 的契约生成管道
 * 落地后改为消费生成物（同 web/src/lib/console.ts 的现状）。
 *
 * 列表页的筛选状态与发往服务端的查询参数是同一份：URL query 的键名与契约参数名一致
 * （page / size / since / until / model / api_key_id / status / request_id），
 * 因此深链、刷新与请求三者天然对齐，不需要两套映射。
 */

/** 一次调用的 token 用量分量；子项包含于主计数。 */
export interface UsageTokens {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cache_write_5m_tokens: number;
  cache_write_1h_tokens: number;
  reasoning_tokens: number;
  server_tool_uses: number;
}

/** 一条请求记录的摘要与归属层字段。 */
export interface RequestItem {
  request_id: string;
  created_at: string;
  status: 'success' | 'failed' | 'cancelled';
  http_status: number;
  upstream_status: number | null;
  failure_class: string | null;
  error_code: string | null;
  duration_ms: number;
  model: string;
  upstream_model: string;
  protocol: string;
  upstream_protocol: string;
  cross_protocol: boolean;
  api_key_id: number;
  usage: UsageTokens | null;
  payload_available: boolean;
  stream?: boolean;
  written_bytes?: number;
}

/** 一次上游尝试。 */
export interface RequestAttempt {
  attempt: number;
  outcome: 'ok' | 'failed' | 'cancelled' | 'skipped';
  upstream_status: number | null;
  failure_class: string | null;
  error_code: string | null;
  cross_protocol: boolean;
  duration_ms: number;
}

/** 脱敏后的报文结构：键名与嵌套层次保留，用户内容替换为类型标记。 */
export type RedactedPayload = Record<string, unknown>;

/** 一条请求记录的完整视图。 */
export interface RequestDetail {
  request: RequestItem;
  attempts: RequestAttempt[];
  request_shape: RedactedPayload | null;
  upstream_request_shape: RedactedPayload | null;
  error_response_shape: RedactedPayload | null;
  rewritten_parts: string[];
  client_ip: string;
  user_agent: string;
}

/** 一个分组维度的请求计数。 */
export interface RequestStatsItem {
  key: string;
  total: number;
  success: number;
  failed: number;
  cancelled: number;
}

/** 请求终态。 */
export type RequestStatus = RequestItem['status'];
/** 一次尝试的结果。 */
export type AttemptOutcome = RequestAttempt['outcome'];
/** 聚合维度。 */
export type StatsGroupBy = 'day' | 'model' | 'status';

/** 终态取值顺序，与契约枚举一致。 */
export const REQUEST_STATUSES: RequestStatus[] = ['success', 'failed', 'cancelled'];
/** 聚合维度取值顺序，与契约枚举一致。 */
export const STATS_GROUPS: StatsGroupBy[] = ['day', 'model', 'status'];
/** 列表默认每页条数。 */
export const DEFAULT_PAGE_SIZE = 20;
/** 契约声明的每页条数上限。 */
export const MAX_PAGE_SIZE = 100;

/** 终态的用户文案；颜色与文字成对出现，不靠颜色单独表达。 */
export const STATUS_LABELS: Record<RequestStatus, string> = {
  success: '成功',
  failed: '失败',
  cancelled: '已取消',
};

/** 尝试结果文案；取值与契约 RequestAttempt.outcome 一致。 */
export const ATTEMPT_OUTCOME_LABELS: Record<AttemptOutcome, string> = {
  ok: '成功',
  failed: '失败',
  cancelled: '已取消',
  skipped: '已跳过',
};

/** 聚合维度文案。 */
export const STATS_GROUP_LABELS: Record<StatsGroupBy, string> = {
  day: '按天',
  model: '按模型',
  status: '按终态',
};

/** RequestListQuery 是列表页的筛选与分页状态。 */
export interface RequestListQuery {
  page: number;
  size: number;
  since: string;
  until: string;
  model: string;
  apiKeyID: string;
  status: RequestStatus | '';
  requestID: string;
}

/** 列表页的默认状态：无筛选、第一页。 */
export const EMPTY_LIST_QUERY: RequestListQuery = {
  page: 1,
  size: DEFAULT_PAGE_SIZE,
  since: '',
  until: '',
  model: '',
  apiKeyID: '',
  status: '',
  requestID: '',
};

/** parseListQuery 从 URL query 解析列表状态；缺失或非法的取值回落默认。 */
export function parseListQuery(search: URLSearchParams): RequestListQuery {
  return {
    page: parsePositiveInt(search.get('page'), EMPTY_LIST_QUERY.page),
    size: clampSize(parsePositiveInt(search.get('size'), DEFAULT_PAGE_SIZE)),
    since: parseMoment(search.get('since')),
    until: parseMoment(search.get('until')),
    model: trimmed(search.get('model')),
    apiKeyID: parseKeyID(search.get('api_key_id')),
    status: parseStatus(search.get('status')),
    requestID: trimmed(search.get('request_id')),
  };
}

/** listQuerySearch 把列表状态序列化为 query；等于默认值的项不写出，保持链接简短。 */
export function listQuerySearch(query: RequestListQuery): URLSearchParams {
  const params = new URLSearchParams();
  if (query.page !== EMPTY_LIST_QUERY.page) params.set('page', String(query.page));
  if (query.size !== EMPTY_LIST_QUERY.size) params.set('size', String(query.size));
  if (query.since !== '') params.set('since', query.since);
  if (query.until !== '') params.set('until', query.until);
  if (query.model !== '') params.set('model', query.model);
  if (query.apiKeyID !== '') params.set('api_key_id', query.apiKeyID);
  if (query.status !== '') params.set('status', query.status);
  if (query.requestID !== '') params.set('request_id', query.requestID);
  return params;
}

/** requestsPath 是列表页路径（含筛选条件），用于深链与「清除筛选」。 */
export function requestsPath(query: RequestListQuery): string {
  const search = listQuerySearch(query).toString();
  return search === '' ? '/requests' : `/requests?${search}`;
}

/** requestDetailPath 是详情页路径：深链可分享，刷新保持。 */
export function requestDetailPath(requestID: string): string {
  return `/requests/${encodeURIComponent(requestID)}`;
}

/** 列表页是否带了任何筛选条件（分页不算筛选）。 */
export function hasFilters(query: RequestListQuery): boolean {
  return (
    query.since !== '' ||
    query.until !== '' ||
    query.model !== '' ||
    query.apiKeyID !== '' ||
    query.status !== '' ||
    query.requestID !== ''
  );
}

/** RequestPage 是一页请求记录，分页三字段取自信封而不是自行推算。 */
export interface RequestPage {
  items: RequestItem[];
  page: number;
  size: number;
  total: number;
}

/** listRequests 拉取一页请求记录。 */
export async function listRequests(query: RequestListQuery): Promise<RequestPage> {
  const env = await requestEnvelope<{ items: RequestItem[] }>(requestsAPI(query));
  if (env.code !== Code.OK) {
    throw new ApiError(env.code, env.message);
  }
  return {
    items: env.data?.items ?? [],
    page: env.page ?? query.page,
    size: env.size ?? query.size,
    total: env.total ?? 0,
  };
}

/** getRequestDetail 拉取单条请求的完整视图。 */
export async function getRequestDetail(requestID: string): Promise<RequestDetail> {
  const data = await request<RequestDetail>(
    `/api/v1/user/requests/${encodeURIComponent(requestID)}`,
  );
  if (!data) {
    throw new ApiError(Code.InternalError, '服务端错误');
  }
  return data;
}

/** RequestStatsQuery 是聚合查询的条件。 */
export interface RequestStatsQuery {
  since: string;
  until: string;
  model: string;
  apiKeyID: string;
  groupBy: StatsGroupBy;
}

/** requestStats 拉取聚合计数；它只回答计数，单条记录走详情端点。 */
export async function requestStats(query: RequestStatsQuery): Promise<RequestStatsItem[]> {
  const params = new URLSearchParams();
  if (query.since !== '') params.set('since', query.since);
  if (query.until !== '') params.set('until', query.until);
  if (query.model !== '') params.set('model', query.model);
  if (query.apiKeyID !== '') params.set('api_key_id', query.apiKeyID);
  params.set('group_by', query.groupBy);
  const data = await request<{ items: RequestStatsItem[] }>(
    `/api/v1/user/requests/stats?${params.toString()}`,
  );
  return data?.items ?? [];
}

/** requestsAPI 是列表端点路径：URL 上的状态直接作为查询参数发出。 */
function requestsAPI(query: RequestListQuery): string {
  const search = listQuerySearch(query).toString();
  return search === '' ? '/api/v1/user/requests' : `/api/v1/user/requests?${search}`;
}

/** formatMoment 把 RFC3339 时刻显示为本地时间；无法解析时原样返回。 */
export function formatMoment(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())}` +
    ` ${pad(parsed.getHours())}:${pad(parsed.getMinutes())}:${pad(parsed.getSeconds())}`
  );
}

/** formatDuration 显示耗时：一秒以下用毫秒，以上用秒并保留一位小数。 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) {
    return '—';
  }
  if (ms < 1000) {
    return `${Math.round(ms)} ms`;
  }
  return `${(ms / 1000).toFixed(1)} s`;
}

/** formatCount 显示整数计数：千分位分隔。 */
export function formatCount(value: number): string {
  if (!Number.isFinite(value)) {
    return '—';
  }
  return String(value).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

/** formatBytes 显示字节数。 */
export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return '—';
  }
  if (value < 1024) {
    return `${value} B`;
  }
  if (value < 1024 * 1024) {
    return `${(value / 1024).toFixed(1)} KiB`;
  }
  return `${(value / 1024 / 1024).toFixed(1)} MiB`;
}

/** toLocalInput 把 RFC3339 时刻转成 datetime-local 输入框的取值；无法解析时回空串。 */
export function toLocalInput(value: string): string {
  const parsed = new Date(value);
  if (value === '' || Number.isNaN(parsed.getTime())) {
    return '';
  }
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())}` +
    `T${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`
  );
}

/** fromLocalInput 把 datetime-local 取值按本机时区转成 RFC3339；空值回空串。 */
export function fromLocalInput(value: string): string {
  if (value === '') {
    return '';
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return '';
  }
  return parsed.toISOString();
}

/** parseStatsGroup 解析聚合维度；非法或缺失回落按天。 */
export function parseStatsGroup(raw: string | null): StatsGroupBy {
  const value = trimmed(raw);
  return (STATS_GROUPS as string[]).includes(value) ? (value as StatsGroupBy) : 'day';
}

/** statusLabel 显示终态文案；未知取值原样显示。 */
export function statusLabel(status: string): string {
  return STATUS_LABELS[status as RequestStatus] ?? status;
}

/** attemptOutcomeLabel 显示尝试结果文案；未知取值原样显示。 */
export function attemptOutcomeLabel(outcome: string): string {
  return ATTEMPT_OUTCOME_LABELS[outcome as AttemptOutcome] ?? outcome;
}

/** payloadNotice 是报文可取性的用户文案：缺失是记录口径，不是接口异常。 */
export function payloadNotice(available: boolean): string {
  return available
    ? '本次保留脱敏报文：键名与结构完整，用户内容已替换为类型标记。'
    : '本次没有可取的报文（成功请求，或已超 7 天）。';
}

/** ShapeDiffKind 是一处差异的种类。 */
export type ShapeDiffKind = 'added' | 'removed' | 'changed';

/** ShapeDiffEntry 是客户端请求结构与上游请求结构之间的一处差异。 */
export interface ShapeDiffEntry {
  path: string;
  kind: ShapeDiffKind;
  from: string;
  to: string;
}

/** summarizeShapeValue 把脱敏报文里的一个取值显示为一行摘要。 */
export function summarizeShapeValue(value: unknown): string {
  if (value === null) {
    return 'null';
  }
  if (Array.isArray(value)) {
    return `[${value.length} 项]`;
  }
  if (typeof value === 'object') {
    const record = value as Record<string, unknown>;
    const marker = record.__redacted;
    if (typeof marker === 'string') {
      const length = record.len;
      return typeof length === 'number' ? `${marker}（${length} 字节）` : marker;
    }
    return `{${Object.keys(record).length} 键}`;
  }
  return JSON.stringify(value) ?? String(value);
}

/**
 * diffPayloadShapes 比较客户端请求结构与上游请求结构，列出网关改写造成的差异。
 *
 * 报文按「落盘即脱敏」处理，值可能是类型标记而不是原文；因此比较的是形状与标记，
 * 不是内容相等。差异列表用于印证 rewritten_parts 的标注。
 */
export function diffPayloadShapes(before: unknown, after: unknown): ShapeDiffEntry[] {
  const entries: ShapeDiffEntry[] = [];
  walkShape('', before, after, entries);
  return entries;
}

/** walkShape 递归比较两个取值，把差异按路径写入 out。 */
function walkShape(path: string, before: unknown, after: unknown, out: ShapeDiffEntry[]): void {
  // 类型标记是叶子：脱敏后用户内容只剩标记与长度，继续下钻只会得到 __redacted / len
  // 两个键，把「内容长度不同」误报成两处结构差异。
  if (isRedactedMarker(before) || isRedactedMarker(after)) {
    if (summarizeShapeValue(before) !== summarizeShapeValue(after)) {
      out.push({
        path: path === '' ? '(根)' : path,
        kind: 'changed',
        from: summarizeShapeValue(before),
        to: summarizeShapeValue(after),
      });
    }
    return;
  }
  if (isRecord(before) && isRecord(after)) {
    const keys = new Set([...Object.keys(before), ...Object.keys(after)]);
    for (const key of [...keys].sort()) {
      const child = path === '' ? key : `${path}.${key}`;
      const inBefore = Object.prototype.hasOwnProperty.call(before, key);
      const inAfter = Object.prototype.hasOwnProperty.call(after, key);
      if (inBefore && !inAfter) {
        out.push({ path: child, kind: 'removed', from: summarizeShapeValue(before[key]), to: '' });
        continue;
      }
      if (!inBefore && inAfter) {
        out.push({ path: child, kind: 'added', from: '', to: summarizeShapeValue(after[key]) });
        continue;
      }
      walkShape(child, before[key], after[key], out);
    }
    return;
  }
  if (Array.isArray(before) && Array.isArray(after)) {
    const length = Math.max(before.length, after.length);
    for (let index = 0; index < length; index++) {
      const child = `${path}[${index}]`;
      if (index >= before.length) {
        out.push({ path: child, kind: 'added', from: '', to: summarizeShapeValue(after[index]) });
        continue;
      }
      if (index >= after.length) {
        out.push({
          path: child,
          kind: 'removed',
          from: summarizeShapeValue(before[index]),
          to: '',
        });
        continue;
      }
      walkShape(child, before[index], after[index], out);
    }
    return;
  }
  if (summarizeShapeValue(before) !== summarizeShapeValue(after)) {
    out.push({
      path: path === '' ? '(根)' : path,
      kind: 'changed',
      from: summarizeShapeValue(before),
      to: summarizeShapeValue(after),
    });
  }
}

/** isRecord 判断取值是否为普通对象（数组与 null 不算）。 */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** isRedactedMarker 判断取值是否为脱敏标记：`{"__redacted": "<类型>", "len": <长度>}`。 */
function isRedactedMarker(value: unknown): boolean {
  return isRecord(value) && typeof value.__redacted === 'string';
}

/** trimmed 去首尾空白；null 视为空串。 */
function trimmed(raw: string | null): string {
  return (raw ?? '').trim();
}

/** parsePositiveInt 解析正整数；非法或缺失回落 fallback。 */
function parsePositiveInt(raw: string | null, fallback: number): number {
  const value = trimmed(raw);
  if (value === '' || !/^[1-9][0-9]*$/.test(value)) {
    return fallback;
  }
  return Number(value);
}

/** clampSize 把每页条数钳制在契约允许的区间内。 */
function clampSize(size: number): number {
  return Math.min(Math.max(size, 1), MAX_PAGE_SIZE);
}

/** parseKeyID 解析密钥 id 过滤；非法取值回落「不过滤」而不是发出必然 400 的请求。 */
function parseKeyID(raw: string | null): string {
  const value = trimmed(raw);
  return /^[1-9][0-9]*$/.test(value) ? value : '';
}

/** parseStatus 解析终态过滤；非法取值回落「不过滤」。 */
function parseStatus(raw: string | null): RequestStatus | '' {
  const value = trimmed(raw);
  return (REQUEST_STATUSES as string[]).includes(value) ? (value as RequestStatus) : '';
}

/** parseMoment 解析 RFC3339 时刻；非法取值回落「不限」，避免把脏参数发往服务端。 */
function parseMoment(raw: string | null): string {
  const value = trimmed(raw);
  if (value === '') {
    return '';
  }
  const rfc3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;
  if (!rfc3339.test(value) || Number.isNaN(new Date(value).getTime())) {
    return '';
  }
  return value;
}
