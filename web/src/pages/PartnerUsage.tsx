import { useEffect, useMemo, type FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { AlertTriangle, ArrowLeft, RotateCw } from 'lucide-react';

import { ApiError, Code } from '../lib/envelope';
import { loginRedirect } from '../lib/navigation';
import { loadUsageStats } from '../lib/partner';
import { fromLocalInput, toLocalInput } from '../lib/requests';
import {
  TOKEN_COLUMNS,
  USAGE_GROUP_KEY_LABELS,
  USAGE_GROUP_LABELS,
  formatChargedAmount,
  formatCount,
  totalUsage,
  type UsageStatsItem,
} from '../lib/usage';
import {
  USAGE_GROUPS,
  parseUsageQuery,
  usageQueryString,
  type UsageQuery,
} from '../lib/usageUrl';

/**
 * 商家域的名下调用量页：按天、模型或密钥把区间内的调用量与应扣量合计起来。
 *
 * 区间、分组维度与过滤全在 URL query 上，刷新与分享保持。口径与账户面的用量页一致：
 * 同一份 URL 状态与同一份参数映射，差别只在作用域是本商家名下的渠道。
 */

/** 无数据时的稳定空数组：每次渲染新建 `?? []` 会让行模型失效。 */
const emptyItems: UsageStatsItem[] = [];

export function PartnerUsage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const query = useMemo(() => parseUsageQuery(params), [params]);

  const stats = useQuery({
    queryKey: ['partner-usage', query.groupBy, query.since, query.until, query.model, query.apiKeyID],
    queryFn: () => loadUsageStats(query),
    retry: false,
  });

  const unauthorized = stats.error instanceof ApiError && stats.error.code === Code.Unauthorized;
  useEffect(() => {
    if (!unauthorized) return;
    navigate(loginRedirect(`${window.location.pathname}${window.location.search}`), {
      replace: true,
    });
  }, [unauthorized, navigate]);

  /** applyQuery 把新条件写进 URL：条件全在 query 上，刷新与分享都能复原。 */
  function applyQuery(next: UsageQuery) {
    const search = usageQueryString(next).toString();
    setParams(new URLSearchParams(search));
  }

  const items = stats.data ?? emptyItems;
  const total = totalUsage(items);

  return (
    <main className="mx-auto max-w-4xl px-4 py-6">
      <header className="flex items-center gap-3">
        <Link
          to="/partner"
          aria-label="返回商家"
          className="flex h-11 w-11 items-center justify-center rounded-lg text-muted hover:text-ink"
        >
          <ArrowLeft size={20} />
        </Link>
        <div>
          <h1 className="text-lg font-bold">名下调用量</h1>
          <p className="text-xs text-muted">本商家名下渠道的调用次数、token 用量与应扣量</p>
        </div>
      </header>

      <UsageFilters query={query} onSubmit={applyQuery} />

      <section className="mt-5">
        <div className="mb-3 flex items-center gap-1 rounded-lg border border-edge bg-surface p-1">
          {USAGE_GROUPS.map((group) => (
            <button
              key={group}
              type="button"
              aria-pressed={query.groupBy === group}
              onClick={() => applyQuery({ ...query, groupBy: group })}
              className={`min-h-9 rounded-md px-3 text-xs ${
                query.groupBy === group ? 'bg-subtle text-ink' : 'text-muted'
              }`}
            >
              按{USAGE_GROUP_LABELS[group]}
            </button>
          ))}
        </div>

        {stats.isPending ? <TableSkeleton /> : null}
        {stats.isError && !unauthorized ? (
          <ErrorState error={stats.error} onRetry={() => void stats.refetch()} busy={stats.isFetching} />
        ) : null}
        {stats.isSuccess && items.length === 0 ? (
          <div className="rounded-2xl border border-edge bg-surface p-8 text-center">
            <p className="text-sm">区间内没有调用记录</p>
            <p className="mt-1 text-xs text-muted">换个区间或分组维度，或确认渠道已启用。</p>
          </div>
        ) : null}
        {stats.isSuccess && items.length > 0 ? (
          <div className="overflow-x-auto rounded-2xl border border-edge bg-surface">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-edge text-left text-xs text-muted">
                  <th scope="col" className="px-3 py-2 font-normal">
                    {USAGE_GROUP_KEY_LABELS[query.groupBy]}
                  </th>
                  <th scope="col" className="px-3 py-2 text-right font-normal">
                    调用次数
                  </th>
                  {TOKEN_COLUMNS.map((column) => (
                    <th
                      key={column.id}
                      scope="col"
                      className={`px-3 py-2 text-right font-normal ${hideClass(column.hideBelow)}`}
                    >
                      {column.label}
                    </th>
                  ))}
                  <th scope="col" className="px-3 py-2 text-right font-normal">
                    应扣量
                  </th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => (
                  <tr key={item.key} className="border-b border-edge last:border-b-0">
                    <td className="px-3 py-3 align-top">
                      <span className="font-mono text-xs">{item.key}</span>
                    </td>
                    <td className="px-3 py-3 text-right align-top tabular-nums">
                      {formatCount(item.calls)}
                    </td>
                    {TOKEN_COLUMNS.map((column) => (
                      <td
                        key={column.id}
                        className={`px-3 py-3 text-right align-top tabular-nums ${hideClass(column.hideBelow)}`}
                      >
                        {formatCount(column.value(item.usage))}
                      </td>
                    ))}
                    <td className="px-3 py-3 text-right align-top tabular-nums">
                      {formatChargedAmount(item.charged_amount)}
                    </td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr className="text-xs text-muted">
                  <td className="px-3 py-3">合计</td>
                  <td className="px-3 py-3 text-right tabular-nums">{formatCount(total.calls)}</td>
                  {TOKEN_COLUMNS.map((column) => (
                    <td
                      key={column.id}
                      className={`px-3 py-3 text-right tabular-nums ${hideClass(column.hideBelow)}`}
                    >
                      {formatCount(column.value(total.tokens))}
                    </td>
                  ))}
                  <td className="px-3 py-3 text-right tabular-nums">
                    {formatChargedAmount(total.chargedAmount)}
                  </td>
                </tr>
              </tfoot>
            </table>
          </div>
        ) : null}
      </section>
    </main>
  );
}

/** UsageFilters 是区间与模型过滤表单：提交即写进 URL。 */
function UsageFilters({ query, onSubmit }: { query: UsageQuery; onSubmit: (next: UsageQuery) => void }) {
  const hasFilters = query.since !== '' || query.until !== '' || query.model !== '';

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const since = String(form.get('since') ?? '');
    const until = String(form.get('until') ?? '');
    onSubmit({
      ...query,
      since: since === '' ? '' : fromLocalInput(since),
      until: until === '' ? '' : fromLocalInput(until),
      model: String(form.get('model') ?? '').trim(),
    });
  }

  return (
    <form onSubmit={submit} className="mt-5 rounded-2xl border border-edge bg-surface p-5">
      <div className="grid gap-3 sm:grid-cols-3">
        <label className="block">
          <span className="mb-1.5 block text-xs text-muted">起始时刻</span>
          <input
            type="datetime-local"
            name="since"
            defaultValue={toLocalInput(query.since)}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-xs text-muted">结束时刻</span>
          <input
            type="datetime-local"
            name="until"
            defaultValue={toLocalInput(query.until)}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-xs text-muted">模型名（可空）</span>
          <input
            name="model"
            defaultValue={query.model}
            placeholder="按客户端请求的模型名匹配"
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </label>
      </div>
      <div className="mt-4 flex gap-2">
        <button
          type="submit"
          className="min-h-11 rounded-lg bg-action px-5 text-sm font-semibold text-white"
        >
          查询
        </button>
        {hasFilters ? (
          <button
            type="button"
            onClick={() => onSubmit({ ...query, since: '', until: '', model: '', apiKeyID: '' })}
            className="min-h-11 rounded-lg border border-edge px-5 text-sm"
          >
            清除过滤
          </button>
        ) : null}
      </div>
    </form>
  );
}

/** ErrorState 是错误态：按业务码分支，重试只由用户手动触发。 */
function ErrorState({
  error,
  onRetry,
  busy,
}: {
  error: unknown;
  onRetry: () => void;
  busy: boolean;
}) {
  const code = error instanceof ApiError ? error.code : Code.InternalError;
  return (
    <div className="rounded-2xl border border-edge bg-surface p-6">
      {code === Code.Forbidden ? (
        <>
          <p className="text-sm">当前身份没有商家域权限。</p>
          <Link
            to="/"
            className="mt-3 inline-flex min-h-11 items-center rounded-lg border border-edge px-4 text-xs"
          >
            返回首页
          </Link>
        </>
      ) : (
        <>
          <p className="flex items-start gap-2 text-xs text-danger">
            <AlertTriangle size={16} className="shrink-0" />
            {code >= Code.InternalError ? '服务端错误，请稍后重试。' : '查询失败，请检查过滤条件。'}
          </p>
          <button
            type="button"
            disabled={busy}
            onClick={onRetry}
            className="mt-3 flex min-h-11 items-center gap-1.5 rounded-lg border border-edge px-4 text-xs disabled:opacity-60"
          >
            <RotateCw size={16} />
            重试
          </button>
        </>
      )}
    </div>
  );
}

/** TableSkeleton 是列表加载态：表格形状的骨架。 */
function TableSkeleton() {
  return (
    <div className="overflow-hidden rounded-2xl border border-edge bg-surface">
      <table className="w-full border-collapse text-sm">
        <tbody aria-hidden="true">
          {[0, 1, 2, 3].map((row) => (
            <tr key={row} className="border-b border-edge last:border-b-0">
              <td className="px-3 py-3">
                <span className="block h-4 w-24 animate-pulse rounded bg-subtle" />
              </td>
              <td className="px-3 py-3 text-right">
                <span className="ml-auto block h-4 w-16 animate-pulse rounded bg-subtle" />
              </td>
              <td className="px-3 py-3 text-right">
                <span className="ml-auto block h-4 w-20 animate-pulse rounded bg-subtle" />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="sr-only">正在加载用量合计</p>
    </div>
  );
}

/** hideClass 把列定义声明的断点档位映射为 Tailwind 类。 */
function hideClass(hideBelow: 'md' | 'lg' | undefined): string {
  if (hideBelow === 'md') return 'hidden md:table-cell';
  if (hideBelow === 'lg') return 'hidden lg:table-cell';
  return '';
}
