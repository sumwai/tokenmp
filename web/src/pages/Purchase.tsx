import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table';
import { AlertTriangle, ArrowLeft, Check, RotateCw, Wallet } from 'lucide-react';

import { formatTimestamp } from '../lib/datetime';
import { formatDecimal, formatUnitAmount } from '../lib/decimal';
import { ApiError, Code } from '../lib/envelope';
import { loginRedirect } from '../lib/navigation';
import {
  DEFAULT_PAGE_SIZE,
  MAX_PAGE_SIZE,
  ordersQueryString,
  parseOrdersQuery,
} from '../lib/purchaseUrl';
import {
  createOrder,
  listOrders,
  listProducts,
  newIdempotencyKey,
  type Order,
  type Product,
} from '../lib/purchase';

/**
 * 充值 / 购买页：商品目录与我的订单。
 *
 * 下单必须幂等：同一意图（档位 + 份数）连续提交复用同一个幂等键，服务端只产生一笔订单；
 * 成功后再下单会换一个新键。金额、数量与折算率都是十进制字符串，展示只做补零与千分位
 * （lib/decimal），不经 `Number` / `parseFloat`。分页状态进 URL query。
 */

/** 列定义声明的窄屏呈现：断点以下隐藏次要列（web/AGENTS.md 移动端）。 */
interface ColumnMeta {
  hideBelow?: 'md' | 'lg';
}

const features = tableFeatures({});
const columnHelper = createColumnHelper<typeof features, Order>();

/** 订单空数组：每次渲染新建 `?? []` 会让行模型失效。 */
const emptyOrders: Order[] = [];

/**
 * UNIT_RATE_SCALE 是折算率的展示小数位，与 account_bucket.unit_rate 的列标度一致。
 * 折算率是比率而不是金额，按金额口径补零会把 0.0000001 显示成 0。
 */
const UNIT_RATE_SCALE = 8;

/** 每页条数可选项，上限与契约 Size 的 maximum 一致。 */
const sizeOptions = [DEFAULT_PAGE_SIZE, 50, MAX_PAGE_SIZE];

/** 份数的合法形态：正数的十进制字符串，至少有一位非零小数。 */
const POSITIVE_DECIMAL = /^\d+(?:\.\d+)?$/;

export function Purchase() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const queryClient = useQueryClient();
  const query = useMemo(() => parseOrdersQuery(params.toString()), [params]);

  const [placed, setPlaced] = useState<string | null>(null);
  /** 幂等键绑定「档位 + 份数」这一意图：同一意图的重复提交复用同一个键。 */
  const intent = useRef<{ fingerprint: string; key: string }>({ fingerprint: '', key: '' });

  const products = useQuery({ queryKey: ['products'], queryFn: listProducts, retry: false });
  const orders = useQuery({
    queryKey: ['orders', query.page, query.size],
    queryFn: () => listOrders(query),
    // 翻页时保留上一页数据，避免表格在加载态与内容之间闪动。
    placeholderData: keepPreviousData,
    retry: false,
  });

  const unauthorized =
    (products.error instanceof ApiError && products.error.code === Code.Unauthorized) ||
    (orders.error instanceof ApiError && orders.error.code === Code.Unauthorized);

  // 会话失效：回登录页并带回当前地址（含分页），登录后回到原处。
  useEffect(() => {
    if (!unauthorized) return;
    navigate(loginRedirect(`${window.location.pathname}${window.location.search}`), { replace: true });
  }, [unauthorized, navigate]);

  const create = useMutation({
    mutationFn: createOrder,
    retry: false,
    onSuccess: (order) => {
      setPlaced(order.product_name);
      // 下单成功即换键：再买一次是新的意图，不能命中上一笔订单。
      intent.current = { fingerprint: '', key: '' };
      void queryClient.invalidateQueries({ queryKey: ['orders'] });
    },
  });

  /** submitOrder 提交一次购买；同一意图（档位 + 份数）复用同一个幂等键。 */
  function submitOrder(product: Product, qty: string) {
    const fingerprint = `${product.id}:${qty}`;
    if (intent.current.fingerprint !== fingerprint) {
      intent.current = { fingerprint, key: newIdempotencyKey() };
    }
    setPlaced(null);
    create.mutate({ product_id: product.id, qty, idempotency_key: intent.current.key });
  }

  function setPage(page: number) {
    setParams(new URLSearchParams(ordersQueryString({ ...query, page })));
  }

  function setSize(size: number) {
    setParams(new URLSearchParams(ordersQueryString({ page: 1, size })));
  }

  const columns = useMemo(
    () =>
      columnHelper.columns([
        columnHelper.accessor('purchased_at', {
          header: '时间',
          meta: { hideBelow: 'md' } satisfies ColumnMeta,
          cell: (ctx) => (
            <time dateTime={ctx.row.original.purchased_at} className="text-xs tabular-nums">
              {formatTimestamp(ctx.row.original.purchased_at)}
            </time>
          ),
        }),
        columnHelper.accessor('product_name', {
          header: '商品',
          cell: (ctx) => <ProductCell order={ctx.row.original} />,
        }),
        columnHelper.accessor('qty', {
          header: '份数',
          cell: (ctx) => (
            <span className="tabular-nums">{formatDecimal(ctx.row.original.qty, 0)}</span>
          ),
        }),
        columnHelper.accessor('price_paid', {
          header: '实付',
          cell: (ctx) => (
            <span className="tabular-nums">{formatDecimal(ctx.row.original.price_paid, 0)}</span>
          ),
        }),
        columnHelper.accessor('total', {
          header: '存量',
          cell: (ctx) => (
            <span className="tabular-nums">
              {formatUnitAmount(ctx.row.original.unit, ctx.row.original.total)}
            </span>
          ),
        }),
        columnHelper.accessor('unit_rate', {
          header: '折算率',
          meta: { hideBelow: 'lg' } satisfies ColumnMeta,
          cell: (ctx) => (
            <span className="tabular-nums">{formatDecimal(ctx.row.original.unit_rate, UNIT_RATE_SCALE)}</span>
          ),
        }),
      ]),
    [],
  );

  const rows = orders.data?.items ?? emptyOrders;
  const table = useTable({ features, columns, data: rows });
  const total = orders.data?.meta.total ?? 0;
  const hasNext = query.page * query.size < total;

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
          <h1 className="text-lg font-bold">充值 / 购买</h1>
          <p className="text-xs text-muted">购买存量包，下单后立即到账</p>
        </div>
      </header>

      {placed !== null ? (
        <section role="status" className="mt-5 rounded-2xl border border-success/40 bg-success/10 p-5 text-sm">
          <h2 className="flex items-center gap-2 font-semibold text-success">
            <Check size={16} />
            下单成功
          </h2>
          <p className="mt-1 text-xs text-muted">
            「{placed}」已到账，可在「我的」页查看包存量。
          </p>
        </section>
      ) : null}

      {create.isError && !unauthorized ? (
        <div className="mt-5">
          <ErrorState error={create.error} onRetry={() => create.reset()} />
        </div>
      ) : null}

      <ProductSection
        products={products.data ?? []}
        isPending={products.isPending}
        isError={products.isError}
        error={products.error}
        unauthorized={unauthorized}
        busy={create.isPending}
        onRetry={() => void products.refetch()}
        onSubmit={submitOrder}
      />

      <section className="mt-6">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-sm font-semibold">我的订单</h2>
          <label className="flex items-center gap-2 text-xs text-muted">
            每页
            <select
              value={query.size}
              onChange={(event) => setSize(Number(event.currentTarget.value))}
              className="min-h-9 rounded-lg border border-edge bg-surface px-2 text-xs text-ink"
            >
              {sizeOptions.map((size) => (
                <option key={size} value={size}>
                  {size}
                </option>
              ))}
            </select>
            条
          </label>
        </div>

        {orders.isPending ? <TableSkeleton /> : null}

        {orders.isError && !unauthorized ? (
          <ErrorState error={orders.error} onRetry={() => void orders.refetch()} busy={orders.isFetching} />
        ) : null}

        {orders.isSuccess && rows.length === 0 ? <EmptyState /> : null}

        {orders.isSuccess && rows.length > 0 ? (
          <div className="overflow-hidden rounded-2xl border border-edge bg-surface">
            <table className="w-full border-collapse text-sm">
              <thead>
                {table.getHeaderGroups().map((group) => (
                  <tr key={group.id} className="border-b border-edge text-left text-xs text-muted">
                    {group.headers.map((header) => (
                      <th
                        key={header.id}
                        scope="col"
                        className={`px-3 py-2 font-normal ${hideClass(columnMeta(header.column))}`}
                      >
                        {header.isPlaceholder ? null : <table.FlexRender header={header} />}
                      </th>
                    ))}
                  </tr>
                ))}
              </thead>
              <tbody>
                {table.getRowModel().rows.map((row) => (
                  <tr key={row.id} className="border-b border-edge last:border-b-0">
                    {row.getAllCells().map((cell) => (
                      <td
                        key={cell.id}
                        className={`px-3 py-3 align-top ${hideClass(columnMeta(cell.column))}`}
                      >
                        <table.FlexRender cell={cell} />
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}

        {orders.isSuccess && total > 0 ? (
          <div className="mt-3 flex items-center justify-between text-xs text-muted">
            <span className="tabular-nums">
              共 {total} 笔 · 第 {query.page} / {Math.max(1, Math.ceil(total / query.size))} 页
            </span>
            <div className="flex gap-2">
              <button
                type="button"
                disabled={query.page <= 1}
                onClick={() => setPage(query.page - 1)}
                className="min-h-9 rounded-lg border border-edge px-3 disabled:opacity-40"
              >
                上一页
              </button>
              <button
                type="button"
                disabled={!hasNext}
                onClick={() => setPage(query.page + 1)}
                className="min-h-9 rounded-lg border border-edge px-3 disabled:opacity-40"
              >
                下一页
              </button>
            </div>
          </div>
        ) : null}
      </section>
    </main>
  );
}

/** ProductSection 是商品目录：卡片视图，卡片视图用卡片骨架。 */
function ProductSection({
  products,
  isPending,
  isError,
  error,
  unauthorized,
  busy,
  onRetry,
  onSubmit,
}: {
  products: Product[];
  isPending: boolean;
  isError: boolean;
  error: unknown;
  unauthorized: boolean;
  busy: boolean;
  onRetry: () => void;
  onSubmit: (product: Product, qty: string) => void;
}) {
  return (
    <section className="mt-6">
      <h2 className="mb-3 text-sm font-semibold">商品目录</h2>

      {isPending ? <CardSkeleton /> : null}

      {isError && !unauthorized ? <ErrorState error={error} onRetry={onRetry} /> : null}

      {!isPending && !isError && products.length === 0 ? (
        <div className="rounded-2xl border border-edge bg-surface p-8 text-center">
          <Wallet size={24} className="mx-auto text-muted" />
          <p className="mt-3 text-sm">当前没有可购买的商品</p>
          <p className="mt-1 text-xs text-muted">平台尚未上架档位，请稍后再来。</p>
        </div>
      ) : null}

      {products.length > 0 ? (
        <div className="grid gap-3 sm:grid-cols-2">
          {products.map((product) => (
            <ProductCard key={product.id} product={product} busy={busy} onSubmit={onSubmit} />
          ))}
        </div>
      ) : null}
    </section>
  );
}

/** ProductCard 是一个档位：口径 + 份数输入 + 购买。 */
function ProductCard({
  product,
  busy,
  onSubmit,
}: {
  product: Product;
  busy: boolean;
  onSubmit: (product: Product, qty: string) => void;
}) {
  const [qty, setQty] = useState('1');
  const [qtyError, setQtyError] = useState<string | null>(null);

  function submit() {
    const trimmed = qty.trim();
    if (!POSITIVE_DECIMAL.test(trimmed) || trimmed.replace(/[0.]/g, '') === '') {
      setQtyError('请填写正数的份数');
      return;
    }
    setQtyError(null);
    onSubmit(product, trimmed);
  }

  return (
    <article className="rounded-2xl border border-edge bg-surface p-5">
      <h3 className="text-sm font-semibold">{product.name}</h3>
      <dl className="mt-3 grid grid-cols-2 gap-y-2 text-xs">
        <dt className="text-muted">每份数量</dt>
        <dd className="text-right tabular-nums">
          {formatUnitAmount(product.unit, product.qty)}
        </dd>
        <dt className="text-muted">售价</dt>
        <dd className="text-right tabular-nums">{formatDecimal(product.price, 0)}</dd>
        <dt className="text-muted">有效期</dt>
        <dd className="text-right">
          {product.validity_days > 0 ? `${product.validity_days} 天` : '不过期'}
        </dd>
        <dt className="text-muted">可用模型</dt>
        <dd className="text-right">
          {product.model_scope === null ? '不限' : product.model_scope.join('、') || '不含任何模型'}
        </dd>
      </dl>
      <div className="mt-4 flex items-end gap-2">
        <label className="flex-1">
          <span className="mb-1.5 block text-xs text-muted">购买份数</span>
          <input
            value={qty}
            inputMode="decimal"
            onChange={(event) => setQty(event.currentTarget.value)}
            aria-invalid={qtyError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </label>
        <button
          type="button"
          disabled={busy}
          onClick={submit}
          className="min-h-11 rounded-lg bg-action px-5 text-sm font-semibold text-white disabled:opacity-60"
        >
          {busy ? '下单中…' : '购买'}
        </button>
      </div>
      {qtyError ? <span className="mt-1 block text-xs text-danger">{qtyError}</span> : null}
      <p className="mt-2 text-xs text-muted">
        实付{' '}
        <span className="tabular-nums">{formatDecimal(product.price, 0)}</span> × 份数
      </p>
    </article>
  );
}

/** ProductCell 在窄屏补一行时间：时间列在该断点以下不显示。 */
function ProductCell({ order }: { order: Order }) {
  return (
    <>
      <span className="font-medium">{order.product_name}</span>
      <span className="mt-0.5 block text-xs text-muted md:hidden">
        {formatTimestamp(order.purchased_at)}
      </span>
    </>
  );
}

/** EmptyState 是订单空态：说明还没有买过，并给出下一步动作。 */
function EmptyState() {
  return (
    <div className="rounded-2xl border border-edge bg-surface p-8 text-center">
      <Wallet size={24} className="mx-auto text-muted" />
      <p className="mt-3 text-sm">还没有订单</p>
      <p className="mt-1 text-xs text-muted">从上面的商品目录选一个档位下单，存量会立即到账。</p>
    </div>
  );
}

/** ErrorState 是错误态：按业务码分支，重试只由用户手动触发。 */
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
  const message = error instanceof ApiError ? error.message : '网络异常，请稍后重试';
  if (code === Code.Forbidden) {
    return (
      <div className="rounded-2xl border border-edge bg-surface p-6">
        <p className="text-sm">当前身份无权购买，或是账户尚未开通。</p>
        <Link
          to="/"
          className="mt-3 inline-flex min-h-11 items-center rounded-lg border border-edge px-4 text-xs"
        >
          返回首页
        </Link>
      </div>
    );
  }
  const hint =
    code === Code.TooManyRequests && error instanceof ApiError && error.retryAfter !== null
      ? `请在 ${error.retryAfter} 秒后重试`
      : code >= Code.InternalError
        ? '服务端错误，请稍后重试。'
        : message;
  return (
    <div className="rounded-2xl border border-edge bg-surface p-6">
      <p className="flex items-start gap-2 text-xs text-danger">
        <AlertTriangle size={16} className="shrink-0" />
        {hint}
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
    </div>
  );
}

/** CardSkeleton 是商品目录的加载骨架：卡片视图用卡片骨架，不与表格骨架串用。 */
function CardSkeleton() {
  return (
    <div className="grid gap-3 sm:grid-cols-2" role="status" aria-label="商品目录正在加载">
      {[0, 1].map((card) => (
        <div key={card} className="rounded-2xl border border-edge bg-surface p-5">
          <div className="h-4 w-32 animate-pulse rounded bg-subtle" />
          <div className="mt-4 space-y-2">
            {[0, 1, 2].map((row) => (
              <div key={row} className="h-3 w-full animate-pulse rounded bg-subtle" />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

/** TableSkeleton 是订单列表的加载态：表格形状的骨架。 */
function TableSkeleton() {
  return (
    <div className="overflow-hidden rounded-2xl border border-edge bg-surface">
      <div className="border-b border-edge px-3 py-2 text-xs text-muted">商品</div>
      <div className="animate-pulse" role="status" aria-label="订单列表正在加载">
        {[0, 1, 2].map((row) => (
          <div key={row} className="flex gap-3 px-3 py-3">
            {[0, 1, 2].map((column) => (
              <div key={column} className="h-4 flex-1 rounded bg-subtle" />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

/** columnMeta 读列定义声明的断点隐藏档位。 */
function columnMeta(column: { columnDef: { meta?: unknown } }): ColumnMeta | undefined {
  return column.columnDef.meta as ColumnMeta | undefined;
}

/** hideClass 把断点档位映射为 Tailwind 类；表头与单元格共用同一份声明。 */
function hideClass(meta: ColumnMeta | undefined): string {
  if (meta?.hideBelow === 'md') return 'hidden md:table-cell';
  if (meta?.hideBelow === 'lg') return 'hidden lg:table-cell';
  return '';
}
