import { useEffect, useMemo, type FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { AlertTriangle, ArrowLeft, RotateCw } from 'lucide-react';

import { ApiError, Code } from '../lib/envelope';
import { loginRedirect } from '../lib/navigation';
import {
  loadSettlement,
  SETTLEMENT_PERIOD_LABELS,
  type PartnerSettlement as SettlementBill,
} from '../lib/partner';
import {
  EMPTY_SETTLEMENT_WINDOW,
  parseSettlementWindow,
  settlementWindowSearch,
  type SettlementWindowQuery,
} from '../lib/partnerUrl';
import { fromLocalInput, toLocalInput } from '../lib/requests';
import { formatDecimal } from '../lib/decimal';

/**
 * 商家域的分账对账单页：本商家一个账期的收益、平台抽成与上游成本。
 *
 * 账期在 URL query 上，刷新与分享保持；不给账期时由服务端按本商家的账期取上一个完整
 * 自然周期。金额与抽成率是十进制字符串：页面只做补零与千分位，不经 `Number` 参与计算。
 */

export function PartnerSettlement() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  // 变量名不叫 window：那会遮蔽全局的 window，登录回跳要用它的 location。
  const selected = useMemo(() => parseSettlementWindow(params), [params]);

  const bill = useQuery({
    queryKey: ['partner-settlement', selected.from, selected.to],
    queryFn: () => loadSettlement(selected),
    retry: false,
  });

  const unauthorized = bill.error instanceof ApiError && bill.error.code === Code.Unauthorized;
  useEffect(() => {
    if (!unauthorized) return;
    navigate(loginRedirect(`${window.location.pathname}${window.location.search}`), {
      replace: true,
    });
  }, [unauthorized, navigate]);

  /** applyWindow 把账期写进 URL：条件全在 query 上，刷新与分享都能复原。 */
  function applyWindow(next: SettlementWindowQuery) {
    setParams(settlementWindowSearch(next));
  }

  return (
    <main className="mx-auto max-w-3xl px-4 py-6">
      <header className="flex items-center gap-3">
        <Link
          to="/partner"
          aria-label="返回商家"
          className="flex h-11 w-11 items-center justify-center rounded-lg text-muted hover:text-ink"
        >
          <ArrowLeft size={20} />
        </Link>
        <div>
          <h1 className="text-lg font-bold">结算对账单</h1>
          <p className="text-xs text-muted">本商家一个账期的卖出总额、平台抽成与上游成本</p>
        </div>
      </header>

      <WindowFilters selected={selected} onSubmit={applyWindow} />

      <section className="mt-5">
        {bill.isPending ? <BillSkeleton /> : null}
        {bill.isError && !unauthorized ? (
          <ErrorState error={bill.error} onRetry={() => void bill.refetch()} busy={bill.isFetching} />
        ) : null}
        {bill.isSuccess ? <BillCard bill={bill.data} /> : null}
      </section>
    </main>
  );
}

/** BillCard 渲染一张对账单：账期、笔数与四项金额。 */
function BillCard({ bill }: { bill: SettlementBill }) {
  const rows: { label: string; value: string; strong?: boolean }[] = [
    { label: '卖出总额', value: formatDecimal(bill.gross_sales, 8) },
    { label: '平台抽成', value: `− ${formatDecimal(bill.commission, 8)}` },
    { label: '上游成本', value: `− ${formatDecimal(bill.upstream_cost, 8)}` },
    { label: '商家收益', value: formatDecimal(bill.payout, 8), strong: true },
  ];

  return (
    <div className="rounded-2xl border border-edge bg-surface">
      <div className="border-b border-edge px-5 py-4">
        <p className="text-xs text-muted">账期</p>
        <p className="mt-1 text-sm">
          按{SETTLEMENT_PERIOD_LABELS[bill.period] ?? bill.period}
          <span className="text-muted">
            {' · '}
            {bill.from} ~ {bill.to}
          </span>
        </p>
        <p className="mt-1 text-xs text-muted">
          成交 {bill.trades} 笔 · 平台抽成率 {bill.commission_rate}
        </p>
      </div>
      <dl className="divide-y divide-edge">
        {rows.map((row) => (
          <div key={row.label} className="flex items-baseline justify-between px-5 py-3">
            <dt className={`text-sm ${row.strong ? 'font-semibold' : 'text-muted'}`}>{row.label}</dt>
            <dd
              className={`tabular-nums ${row.strong ? 'text-base font-semibold' : 'text-sm'}`}
            >
              {row.value}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/** WindowFilters 是账期表单：提交即写进 URL；两侧都不填表示按本商家账期取上一期。 */
function WindowFilters({
  selected,
  onSubmit,
}: {
  selected: SettlementWindowQuery;
  onSubmit: (next: SettlementWindowQuery) => void;
}) {
  const hasWindow = selected.from !== '' || selected.to !== '';

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const from = String(form.get('from') ?? '');
    const to = String(form.get('to') ?? '');
    // 只填一侧等于没有账期：服务端要求两侧成对，这里先收敛成空账期再提交。
    if (from === '' || to === '') {
      onSubmit(EMPTY_SETTLEMENT_WINDOW);
      return;
    }
    onSubmit({ from: fromLocalInput(from), to: fromLocalInput(to) });
  }

  return (
    <form onSubmit={submit} className="mt-5 rounded-2xl border border-edge bg-surface p-5">
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block">
          <span className="mb-1.5 block text-xs text-muted">账期起点（含）</span>
          <input
            type="datetime-local"
            name="from"
            defaultValue={toLocalInput(selected.from)}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-xs text-muted">账期终点（不含）</span>
          <input
            type="datetime-local"
            name="to"
            defaultValue={toLocalInput(selected.to)}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </label>
      </div>
      <p className="mt-2 text-xs text-muted">两个都留空时，按本商家的账期取上一个完整自然周期。</p>
      <div className="mt-4 flex gap-2">
        <button
          type="submit"
          className="min-h-11 rounded-lg bg-action px-5 text-sm font-semibold text-white"
        >
          查询
        </button>
        {hasWindow ? (
          <button
            type="button"
            onClick={() => onSubmit(EMPTY_SETTLEMENT_WINDOW)}
            className="min-h-11 rounded-lg border border-edge px-5 text-sm"
          >
            清除账期
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
            {code >= Code.InternalError ? '服务端错误，请稍后重试。' : '查询失败，请检查账期。'}
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

/** BillSkeleton 是卡片视图的加载态：骨架形状与结果一致。 */
function BillSkeleton() {
  return (
    <div className="rounded-2xl border border-edge bg-surface p-5">
      <span className="block h-4 w-40 animate-pulse rounded bg-subtle" />
      {[0, 1, 2, 3].map((row) => (
        <div key={row} className="mt-4 flex justify-between">
          <span className="block h-4 w-20 animate-pulse rounded bg-subtle" />
          <span className="block h-4 w-28 animate-pulse rounded bg-subtle" />
        </div>
      ))}
      <p className="sr-only">正在加载结算对账单</p>
    </div>
  );
}
