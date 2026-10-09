import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table';
import { AlertTriangle, ArrowLeft, RotateCw } from 'lucide-react';

import { ApiError, Code } from '../lib/envelope';
import { loginRedirect } from '../lib/navigation';
import { fromLocalInput, toLocalInput } from '../lib/requests';
import {
  TOKEN_COLUMNS,
  USAGE_GROUP_KEY_LABELS,
  USAGE_GROUP_LABELS,
  formatChargedAmount,
  formatCount,
  totalUsage,
  usageStats,
  type UsageStatsItem,
  type UsageTotal,
} from '../lib/usage';
import {
  USAGE_GROUPS,
  hasUsageFilters,
  parseUsageQuery,
  usageQueryString,
  type UsageGroupBy,
  type UsageQuery,
} from '../lib/usageUrl';

/**
 * 用量页：按天、模型或密钥把区间内的用量与应扣量合计起来。
 *
 * 区间、分组维度与过滤全在 URL query 上（web/AGENTS.md 的表格规范），刷新与分享保持。
 * 趋势即按天的分组行：当前前端栈没有图表库，页面用表格呈现合计与分组，不引入图表依赖。
 */

/** 列定义声明的窄屏呈现与对齐。 */
interface ColumnMeta {
  hideBelow?: 'md' | 'lg';
  align?: 'left' | 'right';
}

const features = tableFeatures({});
const columnHelper = createColumnHelper<typeof features, UsageStatsItem>();

/** 无数据时的稳定空数组：每次渲染新建 `?? []` 会让行模型失效。 */
const emptyItems: UsageStatsItem[] = [];

/** 调用次数列在窄屏隐藏的断点；表体与合计行两处共用。 */
const CALLS_HIDE_BELOW: ColumnMeta['hideBelow'] = 'md';

/** UsageColumn 是表格的一列：表头、断点与取值口径一处声明，表头、表体与合计行共用。 */
interface UsageColumn {
  id: string;
  label: string;
  hideBelow?: ColumnMeta['hideBelow'];
  align: 'left' | 'right';
  /** 分组行的取值。 */
  row: (item: UsageStatsItem) => ReactNode;
  /** 合计行的取值。 */
  total: (total: UsageTotal) => ReactNode;
}

/** buildColumns 按分组维度给出表格的列；维度值在不同维度下是日期、模型名或密钥 id。 */
function buildColumns(groupBy: UsageGroupBy): UsageColumn[] {
  return [
    {
      id: 'key',
      label: USAGE_GROUP_KEY_LABELS[groupBy],
      align: 'left',
      row: (item) => <KeyCell groupBy={groupBy} item={item} />,
      total: () => '合计',
    },
    {
      id: 'calls',
      label: '调用次数',
      hideBelow: CALLS_HIDE_BELOW,
      align: 'right',
      row: (item) => formatCount(item.calls),
      total: (total) => formatCount(total.calls),
    },
    ...TOKEN_COLUMNS.map((column) => ({
      id: column.id,
      label: column.label,
      hideBelow: column.hideBelow,
      align: 'right' as const,
      row: (item: UsageStatsItem) => formatCount(column.value(item.usage)),
      total: (total: UsageTotal) => formatCount(column.value(total.tokens)),
    })),
    {
      id: 'charged_amount',
      label: '应扣量',
      align: 'right',
      row: (item) => formatChargedAmount(item.charged_amount),
      total: (total) => formatChargedAmount(total.chargedAmount),
    },
  ];
}

export function Usage() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const search = searchParams.toString();
  const query = useMemo(() => parseUsageQuery(new URLSearchParams(search)), [search]);
  const columns = useMemo(() => buildColumns(query.groupBy), [query.groupBy]);

  const stats = useQuery({
    queryKey: ['usage-stats', search],
    queryFn: () => usageStats(query),
    retry: false,
  });

  const unauthorized = stats.error instanceof ApiError && stats.error.code === Code.Unauthorized;

  // 会话失效：回登录页并带回当前地址（含区间与维度），登录后回到原处。
  useEffect(() => {
    if (!unauthorized) return;
    navigate(loginRedirect(`${window.location.pathname}${window.location.search}`), {
      replace: true,
    });
  }, [unauthorized, navigate]);

  // 收敛非法 URL：解析已回落契约默认，把规范化的 query 写回地址栏，让链接、控件与取数三者一致。
  useEffect(() => {
    const canonical = usageQueryString(query).toString();
    if (canonical !== search) {
      setSearchParams(new URLSearchParams(canonical), { replace: true });
    }
  }, [query, search, setSearchParams]);

  /** applyQuery 把新条件写进 URL：条件全在 query 上，刷新与分享都能复原。 */
  function applyQuery(next: UsageQuery) {
    setSearchParams(usageQueryString(next), { replace: false });
  }

  const items = stats.data ?? emptyItems;
  const total = useMemo(() => totalUsage(items), [items]);

  return (
    <main className="mx-auto max-w-4xl px-4 py-6">
      <header className="flex items-center gap-3">
        <Link
          to="/"
          aria-label="返回首页"
          className="flex h-11 w-11 items-center justify-center rounded-lg text-muted hover:text-ink"
        >
          <ArrowLeft size={20} />
        </Link>
        <div>
          <h1 className="text-lg font-bold">用量</h1>
          <p className="text-xs text-muted">
            按天、模型或密钥把区间内的用量与应扣量合计起来；合计与流水明细同源，逐行相加一致。
          </p>
        </div>
      </header>

      <FilterBar
        query={query}
        onApply={applyQuery}
        onReset={() => setSearchParams(new URLSearchParams())}
      />

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <span className="text-xs text-muted">分组维度</span>
        {USAGE_GROUPS.map((group) => (
          <button
            key={group}
            type="button"
            aria-pressed={query.groupBy === group}
            onClick={() => applyQuery({ ...query, groupBy: group })}
            className={`min-h-9 rounded-lg border px-3 text-xs ${
              query.groupBy === group ? 'border-action text-action' : 'border-edge text-muted'
            }`}
          >
            {USAGE_GROUP_LABELS[group]}
          </button>
        ))}
      </div>

      <section className="mt-4">
        {stats.isPending ? <TableSkeleton /> : null}

        {stats.isError && !unauthorized ? (
          <ErrorState
            error={stats.error}
            onRetry={() => void stats.refetch()}
            busy={stats.isFetching}
          />
        ) : null}

        {stats.isSuccess && items.length === 0 ? (
          <EmptyState
            filtered={hasUsageFilters(query)}
            onClear={() => setSearchParams(new URLSearchParams())}
          />
        ) : null}

        {stats.isSuccess && items.length > 0 ? (
          <UsageTable columns={columns} items={items} total={total} groupBy={query.groupBy} />
        ) : null}
      </section>
    </main>
  );
}

/** UsageTable 是合计表：分组行按维度升序，末尾一行是页面合计。 */
function UsageTable({
  columns,
  items,
  total,
  groupBy,
}: {
  columns: UsageColumn[];
  items: UsageStatsItem[];
  total: UsageTotal;
  groupBy: UsageGroupBy;
}) {
  const table = useTable({ features, columns: toTableColumns(columns), data: items });

  return (
    <div className="overflow-x-auto rounded-2xl border border-edge bg-surface">
      <table className="w-full border-collapse text-sm">
        <thead>
          {table.getHeaderGroups().map((group) => (
            <tr key={group.id} className="border-b border-edge text-xs text-muted">
              {group.headers.map((header) => {
                const meta = columnMeta(header.column);
                return (
                  <th
                    key={header.id}
                    scope="col"
                    className={`px-3 py-2 font-normal ${hideClass(meta)} ${alignClass(meta)}`}
                  >
                    {header.isPlaceholder ? null : <table.FlexRender header={header} />}
                  </th>
                );
              })}
            </tr>
          ))}
        </thead>
        <tbody>
          {table.getRowModel().rows.map((row) => (
            <tr key={row.id} className="border-b border-edge last:border-b-0">
              {row.getAllCells().map((cell) => {
                const meta = columnMeta(cell.column);
                return (
                  <td
                    key={cell.id}
                    className={`px-3 py-3 align-top ${hideClass(meta)} ${alignClass(meta)}`}
                  >
                    <table.FlexRender cell={cell} />
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
        <tfoot>
          <tr className="border-t border-edge font-semibold">
            {columns.map((column) => (
              <td
                key={column.id}
                className={`px-3 py-3 align-top ${
                  column.hideBelow === 'md'
                    ? 'hidden md:table-cell'
                    : column.hideBelow === 'lg'
                      ? 'hidden lg:table-cell'
                      : ''
                } ${column.align === 'right' ? 'text-right tabular-nums' : 'text-left'}`}
              >
                {column.total(total)}
              </td>
            ))}
          </tr>
        </tfoot>
      </table>
      <p className="border-t border-edge px-3 py-2 text-xs text-muted">
        {groupBy === 'day'
          ? '日期取落库时刻所在的自然日（服务端时区），页面不做时区换算。'
          : '分组值为写入流水时的取值；区间按写入时刻（含两端）过滤。'}
      </p>
    </div>
  );
}

/** toTableColumns 把列规格转成 TanStack 的列定义：取值与断点都来自同一份规格。 */
function toTableColumns(columns: UsageColumn[]) {
  return columns.map((column) =>
    columnHelper.display({
      id: column.id,
      header: column.label,
      meta: { hideBelow: column.hideBelow, align: column.align } satisfies ColumnMeta,
      cell: (ctx) => column.row(ctx.row.original),
    }),
  );
}

/** KeyCell 是维度列：窄屏补一行调用次数（调用次数列在该断点以下隐藏）。 */
function KeyCell({ groupBy, item }: { groupBy: UsageGroupBy; item: UsageStatsItem }) {
  return (
    <>
      <span className="font-mono text-xs">{groupBy === 'api_key' ? `#${item.key}` : item.key}</span>
      <span className="mt-0.5 block text-xs text-muted tabular-nums md:hidden">
        调用 {formatCount(item.calls)} 次
      </span>
    </>
  );
}

/**
 * FilterBar 是区间与过滤区：输入先落在本地状态，点「应用」才写进 URL 并发起请求。
 *
 * 时间输入用 datetime-local，提交前按本机时区转回 RFC3339（契约的参数形态）。
 */
function FilterBar({
  query,
  onApply,
  onReset,
}: {
  query: UsageQuery;
  onApply: (next: UsageQuery) => void;
  onReset: () => void;
}) {
  const [draft, setDraft] = useState(query);

  // URL 变化（含深链、后退）时把输入框同步回来，避免输入框与结果不一致。
  useEffect(() => {
    setDraft(query);
  }, [query]);

  function submit(event: FormEvent) {
    event.preventDefault();
    onApply({
      ...draft,
      since: fromLocalInput(draft.since),
      until: fromLocalInput(draft.until),
    });
  }

  return (
    <form
      onSubmit={submit}
      className="mt-4 rounded-2xl border border-edge bg-surface p-4"
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <FilterInput
          label="起始时刻（可空）"
          type="datetime-local"
          value={toLocalInput(draft.since)}
          onChange={(value) => setDraft({ ...draft, since: value })}
        />
        <FilterInput
          label="结束时刻（可空）"
          type="datetime-local"
          value={toLocalInput(draft.until)}
          onChange={(value) => setDraft({ ...draft, until: value })}
        />
        <FilterInput
          label="模型名（可空）"
          value={draft.model}
          placeholder="精确匹配，如 gpt-4o-mini"
          onChange={(value) => setDraft({ ...draft, model: value })}
        />
        <FilterInput
          label="密钥 id（可空）"
          value={draft.apiKeyID}
          placeholder="正整数"
          onChange={(value) => setDraft({ ...draft, apiKeyID: value })}
        />
      </div>

      <div className="mt-4 flex gap-2">
        <button
          type="submit"
          className="min-h-10 rounded-lg bg-action px-4 text-sm font-semibold text-white"
        >
          应用条件
        </button>
        <button
          type="button"
          onClick={onReset}
          className="min-h-10 rounded-lg border border-edge px-4 text-sm"
        >
          清除条件
        </button>
      </div>
    </form>
  );
}

/** FilterInput 是条件输入框。 */
function FilterInput({
  label,
  value,
  onChange,
  type = 'text',
  placeholder,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: string;
  placeholder?: string;
}) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-muted">{label}</span>
      <input
        type={type}
        value={value}
        placeholder={placeholder}
        onChange={(event) => onChange(event.currentTarget.value)}
        className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
      />
    </label>
  );
}

/** TableSkeleton 是表格加载态：表格形状的骨架，不用卡片骨架。 */
function TableSkeleton() {
  return (
    <div className="overflow-hidden rounded-2xl border border-edge bg-surface">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="border-b border-edge text-left text-xs text-muted">
            <th scope="col" className="px-3 py-2 font-normal">
              分组
            </th>
            <th scope="col" className="px-3 py-2 font-normal">
              调用次数
            </th>
            <th scope="col" className="px-3 py-2 font-normal text-right">
              应扣量
            </th>
          </tr>
        </thead>
        <tbody aria-hidden="true">
          {[0, 1, 2, 3, 4].map((row) => (
            <tr key={row} className="border-b border-edge last:border-b-0">
              <td className="px-3 py-3">
                <span className="block h-4 w-24 animate-pulse rounded bg-subtle" />
              </td>
              <td className="px-3 py-3">
                <span className="block h-4 w-16 animate-pulse rounded bg-subtle" />
              </td>
              <td className="px-3 py-3">
                <span className="block h-4 w-20 animate-pulse rounded bg-subtle" />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="sr-only">正在加载用量合计</p>
    </div>
  );
}

/** EmptyState 是统一空态：区分「一条都没有」与「筛选无结果」，各给下一步动作。 */
function EmptyState({ filtered, onClear }: { filtered: boolean; onClear: () => void }) {
  return (
    <div className="rounded-2xl border border-edge bg-surface px-4 py-10 text-center">
      <p className="text-sm font-semibold">
        {filtered ? '当前条件下没有用量合计' : '这个区间还没有用量'}
      </p>
      <p className="mt-1 text-xs text-muted">
        {filtered
          ? '区间或过滤条件可能太窄，放宽条件或清除后再看。'
          : '签发密钥并调用一次模型后，用量会在这里按维度合计出来。'}
      </p>
      <div className="mt-4 flex justify-center gap-2">
        {filtered ? (
          <button
            type="button"
            onClick={onClear}
            className="min-h-10 rounded-lg border border-edge px-4 text-sm"
          >
            清除条件
          </button>
        ) : null}
        <Link
          to="/"
          className="flex min-h-10 items-center rounded-lg border border-edge px-4 text-sm"
        >
          返回首页
        </Link>
      </div>
    </div>
  );
}

/** ErrorState 是错误态：按业务码分支，重试只由用户手动触发（不自动循环重试）。 */
function ErrorState({
  error,
  onRetry,
  busy = false,
}: {
  error: unknown;
  onRetry: () => void;
  busy?: boolean;
}) {
  const code = error instanceof ApiError ? error.code : Code.InternalError;
  if (code === Code.Forbidden) {
    return (
      <div className="rounded-2xl border border-edge bg-surface p-6">
        <p className="text-sm">当前身份无权查看用量。</p>
        <Link
          to="/"
          className="mt-3 inline-flex min-h-11 items-center rounded-lg border border-edge px-4 text-xs"
        >
          返回首页
        </Link>
      </div>
    );
  }
  return (
    <div className="rounded-2xl border border-edge bg-surface p-6">
      <ErrorNote error={error instanceof ApiError ? error : new ApiError(code, '服务端错误')} />
      <button
        type="button"
        disabled={busy}
        onClick={onRetry}
        className="mt-3 flex min-h-11 items-center gap-1.5 rounded-lg border border-edge px-4 text-xs disabled:opacity-60"
      >
        <RotateCw size={16} />
        重试
      </button>
    </div>
  );
}

/** ErrorNote 显示失败文案：429 按 Retry-After 推算等待时间，5xx 给服务端文案。 */
function ErrorNote({ error }: { error: ApiError }) {
  const text =
    error.code === Code.TooManyRequests
      ? error.retryAfter === null
        ? `${error.message}（请求过于频繁，请稍后重试）`
        : `${error.message}（请求过于频繁，请在 ${error.retryAfter} 秒后重试）`
      : error.code >= Code.InternalError
        ? '服务端错误，请稍后重试。'
        : error.message;
  return (
    <p className="flex items-start gap-2 text-xs text-danger">
      <AlertTriangle size={16} className="shrink-0" />
      {text}
    </p>
  );
}

/** columnMeta 读列定义声明的断点与对齐。 */
function columnMeta(column: { columnDef: { meta?: unknown } }): ColumnMeta | undefined {
  return column.columnDef.meta as ColumnMeta | undefined;
}

/** hideClass 把断点档位映射为 Tailwind 类；表头与单元格共用同一份声明。 */
function hideClass(meta: ColumnMeta | undefined): string {
  if (meta?.hideBelow === 'md') return 'hidden md:table-cell';
  if (meta?.hideBelow === 'lg') return 'hidden lg:table-cell';
  return '';
}

/** alignClass 把对齐声明映射为 Tailwind 类；数字列右对齐并等宽。 */
function alignClass(meta: ColumnMeta | undefined): string {
  return meta?.align === 'right' ? 'text-right tabular-nums' : 'text-left';
}
