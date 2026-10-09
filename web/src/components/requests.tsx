import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';

import { ApiError, Code } from '../lib/envelope';
import {
  attemptOutcomeLabel,
  diffPayloadShapes,
  formatBytes,
  formatCount,
  formatDuration,
  formatMoment,
  payloadNotice,
  statusLabel,
  summarizeShapeValue,
  type RequestAttempt,
  type RequestItem,
  type RequestStatsItem,
  type RequestStatus,
  type ShapeDiffEntry,
} from '../lib/requests';

/**
 * 请求记录页的视图件：状态徽标、三态面板、尝试时间线、报文差异与分页器。
 *
 * 组件只做展示，数据与状态由页面持有（web/AGENTS.md：页面状态进 URL）。
 */

/** 状态色调：颜色与文字成对出现，不靠颜色单独表达。 */
const STATUS_TONES: Record<RequestStatus, string> = {
  success: 'border-success/40 bg-success/10 text-success',
  failed: 'border-danger/40 bg-danger/10 text-danger',
  cancelled: 'border-warn/40 bg-warn/10 text-warn',
};

/** StatusBadge 显示请求终态。 */
export function StatusBadge({ status }: { status: string }) {
  const tone = STATUS_TONES[status as RequestStatus] ?? 'border-edge bg-subtle text-muted';
  return (
    <span className={`inline-flex items-center rounded-md border px-2 py-0.5 text-xs ${tone}`}>
      {statusLabel(status)}
    </span>
  );
}

/** SectionCard 是一段内容的容器：标题 + 可选说明。 */
export function SectionCard({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <section className="mt-4 rounded-2xl border border-edge bg-surface p-4 sm:p-5">
      <h2 className="text-sm font-semibold">{title}</h2>
      {description ? <p className="mt-1 text-xs text-muted">{description}</p> : null}
      <div className="mt-3">{children}</div>
    </section>
  );
}

/** Field 是一条「标签 + 取值」；取值等宽显示以便比对标识与数字。 */
export function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-xs text-muted">{label}</div>
      <div className="mt-0.5 truncate font-mono text-sm tabular-nums">{children}</div>
    </div>
  );
}

/** FieldGrid 是字段网格，窄屏单列、宽屏三列。 */
export function FieldGrid({ children }: { children: ReactNode }) {
  return <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">{children}</div>;
}

/** 表格骨架：列表视图用表格骨架，不用卡片骨架。 */
export function ListSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <div aria-busy="true" className="overflow-hidden rounded-2xl border border-edge bg-surface">
      {Array.from({ length: rows }, (_, index) => (
        <div
          key={index}
          className="flex h-[72px] items-center gap-3 border-b border-edge px-4 last:border-b-0"
        >
          <div className="h-4 w-40 animate-pulse rounded bg-subtle" />
          <div className="h-4 w-20 animate-pulse rounded bg-subtle" />
          <div className="ml-auto h-4 w-16 animate-pulse rounded bg-subtle" />
        </div>
      ))}
    </div>
  );
}

/** EmptyState 是统一空态：说明为什么空，并给出下一步动作。 */
export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="rounded-2xl border border-edge bg-surface px-4 py-10 text-center">
      <p className="text-sm font-semibold">{title}</p>
      <p className="mt-1 text-xs text-muted">{description}</p>
      {action ? <div className="mt-4 flex justify-center">{action}</div> : null}
    </div>
  );
}

/** ErrorPanel 按错误码分支；5xx 与 429 都只给手动重试，不自动循环重试。 */
export function ErrorPanel({ error, onRetry }: { error: ApiError; onRetry: () => void }) {
  const hint =
    error.code === Code.TooManyRequests
      ? '请求过于频繁，请稍后再试。'
      : error.code >= 500
        ? '服务端暂时不可用。'
        : error.message;
  return (
    <div role="alert" className="rounded-2xl border border-danger/40 bg-danger/5 px-4 py-6 text-center">
      <p className="text-sm font-semibold text-danger">{hint}</p>
      {error.code === Code.TooManyRequests || error.code >= 500 ? (
        <p className="mt-1 text-xs text-muted">服务端文案：{error.message}</p>
      ) : null}
      <button
        type="button"
        onClick={onRetry}
        className="mt-4 min-h-10 rounded-lg border border-edge bg-surface px-4 text-sm hover:border-action"
      >
        重新加载
      </button>
    </div>
  );
}

/** Pager 是偏移分页控件：页码与总数取自信封。 */
export function Pager({
  page,
  size,
  total,
  onPage,
}: {
  page: number;
  size: number;
  total: number;
  onPage: (page: number) => void;
}) {
  const lastPage = Math.max(Math.ceil(total / Math.max(size, 1)), 1);
  return (
    <div className="mt-3 flex items-center justify-between text-sm">
      <span className="text-xs text-muted tabular-nums">
        第 {page} / {lastPage} 页 · 共 {formatCount(total)} 条
      </span>
      <div className="flex gap-2">
        <button
          type="button"
          disabled={page <= 1}
          onClick={() => onPage(page - 1)}
          className="min-h-10 rounded-lg border border-edge px-3 text-sm disabled:opacity-40"
        >
          上一页
        </button>
        <button
          type="button"
          disabled={page >= lastPage}
          onClick={() => onPage(page + 1)}
          className="min-h-10 rounded-lg border border-edge px-3 text-sm disabled:opacity-40"
        >
          下一页
        </button>
      </div>
    </div>
  );
}

/** RequestRow 是列表中的一行，整行可点进入详情。 */
export function RequestRow({ item }: { item: RequestItem }) {
  return (
    <Link
      to={`/requests/${encodeURIComponent(item.request_id)}`}
      className="flex h-[72px] items-center gap-3 border-b border-edge px-4 last:border-b-0 hover:bg-subtle"
    >
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <StatusBadge status={item.status} />
          <span className="truncate text-sm font-medium">{item.model}</span>
          {item.upstream_model !== item.model ? (
            <span className="shrink-0 text-xs text-muted">→ {item.upstream_model}</span>
          ) : null}
        </div>
        <div className="mt-1 flex items-center gap-2 truncate text-xs text-muted tabular-nums">
          <span>{formatMoment(item.created_at)}</span>
          <span>· {formatDuration(item.duration_ms)}</span>
          <span>· HTTP {item.http_status}</span>
          <span>· 密钥 {item.api_key_id}</span>
          <span>· {item.payload_available ? '有脱敏报文' : '无报文'}</span>
        </div>
      </div>
      <div className="hidden shrink-0 text-right text-xs text-muted tabular-nums sm:block">
        {item.usage ? (
          <>
            <div>输入 {formatCount(item.usage.input_tokens)}</div>
            <div>输出 {formatCount(item.usage.output_tokens)}</div>
          </>
        ) : (
          <div>未取得用量</div>
        )}
      </div>
      <span aria-hidden="true" className="shrink-0 text-muted">
        ›
      </span>
    </Link>
  );
}

/** StatsList 显示聚合计数：条数与构成，宽度按占比表达。 */
export function StatsList({ items, emptyText }: { items: RequestStatsItem[]; emptyText: string }) {
  if (items.length === 0) {
    return <p className="text-xs text-muted">{emptyText}</p>;
  }
  const max = Math.max(...items.map((item) => item.total), 1);
  return (
    <ul className="flex flex-col gap-2">
      {items.map((item) => (
        <li key={item.key} className="text-xs">
          <div className="flex items-center justify-between gap-3">
            <span className="truncate font-mono">{item.key}</span>
            <span className="shrink-0 tabular-nums text-muted">
              {formatCount(item.total)} 次 · 成功 {formatCount(item.success)} · 失败{' '}
              {formatCount(item.failed)} · 已取消 {formatCount(item.cancelled)}
            </span>
          </div>
          <div className="mt-1 h-1.5 w-full rounded bg-subtle">
            <div
              className="h-1.5 rounded bg-action"
              style={{ width: `${Math.round((item.total / max) * 100)}%` }}
            />
          </div>
        </li>
      ))}
    </ul>
  );
}

/** AttemptTimeline 显示尝试时间线：重试与渠道回退各占一条。 */
export function AttemptTimeline({ attempts }: { attempts: RequestAttempt[] }) {
  if (attempts.length === 0) {
    return <p className="text-xs text-muted">本次没有上游尝试记录。</p>;
  }
  return (
    <ol className="flex flex-col gap-3">
      {attempts.map((attempt) => (
        <li key={attempt.attempt} className="border-l-2 border-edge pl-3">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="font-semibold tabular-nums">第 {attempt.attempt} 次</span>
            <span className="text-xs">{attemptOutcomeLabel(attempt.outcome)}</span>
            {attempt.cross_protocol ? (
              <span className="rounded border border-edge px-1.5 text-xs text-muted">
                跨协议
              </span>
            ) : null}
            <span className="text-xs text-muted tabular-nums">
              {formatDuration(attempt.duration_ms)}
            </span>
          </div>
          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted tabular-nums">
            <span>上游状态码 {attempt.upstream_status ?? '未取得'}</span>
            <span>失败分类 {attempt.failure_class ?? '无'}</span>
            <span>错误码 {attempt.error_code ?? '无'}</span>
          </div>
        </li>
      ))}
    </ol>
  );
}

/** ShapeDiff 显示客户端请求结构与上游请求结构的差异，并标注网关改写过的部分。 */
export function ShapeDiff({
  requestShape,
  upstreamShape,
  rewrittenParts,
  payloadAvailable,
}: {
  requestShape: unknown;
  upstreamShape: unknown;
  rewrittenParts: string[];
  payloadAvailable: boolean;
}) {
  if (!payloadAvailable) {
    return <p className="text-sm text-muted">{payloadNotice(false)}</p>;
  }
  const entries = diffPayloadShapes(requestShape, upstreamShape);
  return (
    <div className="flex flex-col gap-3">
      {rewrittenParts.length > 0 ? (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-muted">改写标注</span>
          {rewrittenParts.map((part) => (
            <span key={part} className="rounded border border-edge px-1.5 py-0.5 font-mono text-xs">
              {part}
            </span>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted">本次没有改写标注。</p>
      )}
      {entries.length === 0 ? (
        <p className="text-sm text-muted">两侧结构一致，没有可列出的差异。</p>
      ) : (
        <ul className="flex flex-col gap-1.5 font-mono text-xs">
          {entries.map((entry) => (
            <li key={`${entry.kind}:${entry.path}`} className="flex flex-wrap gap-x-2">
              <span className="text-muted">{entry.path}</span>
              <span>{diffKindLabel(entry.kind)}</span>
              <span className="tabular-nums">
                {entry.from}
                {entry.to ? ` → ${entry.to}` : ''}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** diffKindLabel 是一处差异的文案。 */
function diffKindLabel(kind: ShapeDiffEntry['kind']): string {
  switch (kind) {
    case 'added':
      return '新增';
    case 'removed':
      return '缺失';
    default:
      return '改写';
  }
}

/** ShapePreview 显示脱敏报文的结构摘要：键名与形状，不含用户内容。 */
export function ShapePreview({ value }: { value: unknown }) {
  if (value === null || value === undefined) {
    return <p className="text-sm text-muted">无报文。</p>;
  }
  const entries = flattenShape(value);
  return (
    <ul className="flex flex-col gap-1 font-mono text-xs">
      {entries.map((entry) => (
        <li key={entry.path} className="flex flex-wrap gap-x-2">
          <span className="text-muted">{entry.path}</span>
          <span className="tabular-nums">{entry.summary}</span>
        </li>
      ))}
    </ul>
  );
}

/** flattenShape 把报文结构摊平成「路径 + 取值摘要」列表。 */
function flattenShape(value: unknown, path = ''): { path: string; summary: string }[] {
  const rows: { path: string; summary: string }[] = [];
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    const record = value as Record<string, unknown>;
    const marker = record.__redacted;
    if (typeof marker === 'string') {
      rows.push({ path: path || '(根)', summary: summarizeShapeValue(record) });
      return rows;
    }
    for (const key of Object.keys(record).sort()) {
      rows.push(...flattenShape(record[key], path === '' ? key : `${path}.${key}`));
    }
    return rows;
  }
  if (Array.isArray(value)) {
    rows.push({ path: path || '(根)', summary: summarizeShapeValue(value) });
    return rows;
  }
  rows.push({ path: path || '(根)', summary: summarizeShapeValue(value) });
  return rows;
}

/** UsageSummary 显示用量分量；未取得用量时说明原因。 */
export function UsageSummary({ item }: { item: RequestItem }) {
  if (!item.usage) {
    return <p className="text-xs text-muted">未取得用量（上游未返回或调用未完成）。</p>;
  }
  const usage = item.usage;
  const rows: { label: string; value: number }[] = [
    { label: '输入 token', value: usage.input_tokens },
    { label: '输出 token', value: usage.output_tokens },
    { label: '缓存读', value: usage.cache_read_tokens },
    { label: '缓存写', value: usage.cache_write_tokens },
    { label: '缓存写（5 分钟档）', value: usage.cache_write_5m_tokens },
    { label: '缓存写（1 小时档）', value: usage.cache_write_1h_tokens },
    { label: '推理 token', value: usage.reasoning_tokens },
    { label: '服务端工具执行', value: usage.server_tool_uses },
  ];
  return (
    <FieldGrid>
      {rows.map((row) => (
        <Field key={row.label} label={row.label}>
          {formatCount(row.value)}
        </Field>
      ))}
      <Field label="已写字节数">{formatBytes(item.written_bytes ?? 0)}</Field>
    </FieldGrid>
  );
}
