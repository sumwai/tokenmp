import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table';
import { AlertTriangle, ArrowLeft, Check, Copy, KeyRound, Plus, RotateCw, X } from 'lucide-react';

import { ApiError, Code } from '../lib/envelope';
import { createKey, listKeys, revokeKey, type ApiKey, type CreatedApiKey } from '../lib/keys';
import { DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE, keysQueryString, parseKeysQuery, type KeysQuery } from '../lib/keysUrl';
import { loginRedirect } from '../lib/navigation';

/** 密钥管理页：列表、创建与吊销。筛选与分页全在 URL query 上。 */

/** 列定义声明的窄屏呈现：断点以下隐藏次要列（web/AGENTS.md 移动端）。 */
interface ColumnMeta {
  hideBelow?: 'md' | 'lg';
}

const features = tableFeatures({});
const columnHelper = createColumnHelper<typeof features, ApiKey>();

/** 无数据时的稳定空数组：每次渲染新建 `?? []` 会让行模型失效。 */
const emptyKeys: ApiKey[] = [];

/** 每页条数可选项，上限与契约 Size 的 maximum 一致。 */
const sizeOptions = [DEFAULT_PAGE_SIZE, 50, MAX_PAGE_SIZE];

type EnabledFilter = boolean | undefined;

export function Keys() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const queryClient = useQueryClient();
  const query = useMemo(() => parseKeysQuery(params.toString()), [params]);

  const [created, setCreated] = useState<CreatedApiKey | null>(null);
  const [pendingRevoke, setPendingRevoke] = useState<ApiKey | null>(null);

  const keys = useQuery({
    queryKey: ['keys', query.page, query.size, query.enabled ?? 'all'],
    queryFn: () => listKeys(query),
    // 翻页时保留上一页数据，避免表格在加载态与内容之间闪动。
    placeholderData: keepPreviousData,
    retry: false,
  });

  const unauthorized = keys.error instanceof ApiError && keys.error.code === Code.Unauthorized;

  // 会话失效：回登录页并带回当前地址（含筛选与分页），登录后回到原处。
  useEffect(() => {
    if (!unauthorized) return;
    navigate(loginRedirect(`${window.location.pathname}${window.location.search}`), {
      replace: true,
    });
  }, [unauthorized, navigate]);

  const create = useMutation({
    mutationFn: createKey,
    retry: false,
    onSuccess: (data) => {
      setCreated(data);
      void queryClient.invalidateQueries({ queryKey: ['keys'] });
    },
  });

  const revoke = useMutation({
    mutationFn: revokeKey,
    retry: false,
    onSuccess: () => {
      setPendingRevoke(null);
      void queryClient.invalidateQueries({ queryKey: ['keys'] });
    },
  });

  /** applyQuery 把新条件写进 URL：条件全在 query 上，刷新与分享都能复原。 */
  function applyQuery(next: KeysQuery) {
    setParams(new URLSearchParams(keysQueryString(next)));
  }

  function setFilter(enabled: EnabledFilter) {
    applyQuery({ page: 1, size: query.size, enabled });
  }

  function setPage(page: number) {
    applyQuery({ ...query, page });
  }

  function setSize(size: number) {
    applyQuery({ page: 1, size, enabled: query.enabled });
  }

  const columns = useMemo(
    () =>
      columnHelper.columns([
        columnHelper.accessor('name', {
          header: '名称',
          cell: (ctx) => <NameCell apiKey={ctx.row.original} />,
        }),
        columnHelper.accessor('key_prefix', {
          header: '密钥前缀',
          meta: { hideBelow: 'md' } satisfies ColumnMeta,
          cell: (ctx) => (
            <code className="font-mono text-xs text-muted">{ctx.row.original.key_prefix}…</code>
          ),
        }),
        columnHelper.accessor('enabled', {
          header: '状态',
          cell: (ctx) => <StatusTag enabled={ctx.row.original.enabled} />,
        }),
        columnHelper.accessor('last_used_at', {
          header: '最近使用',
          meta: { hideBelow: 'lg' } satisfies ColumnMeta,
          cell: (ctx) => <TimeCell value={ctx.row.original.last_used_at} empty="从未使用" />,
        }),
        columnHelper.accessor('expires_at', {
          header: '过期时间',
          meta: { hideBelow: 'lg' } satisfies ColumnMeta,
          cell: (ctx) => <TimeCell value={ctx.row.original.expires_at} empty="不过期" />,
        }),
        columnHelper.display({
          id: 'actions',
          header: '操作',
          cell: (ctx) =>
            ctx.row.original.enabled ? (
              <button
                type="button"
                onClick={() => setPendingRevoke(ctx.row.original)}
                className="min-h-9 rounded-lg border border-edge px-3 text-xs text-danger hover:border-danger"
              >
                吊销
              </button>
            ) : null,
        }),
      ]),
    [],
  );

  const rows = keys.data?.items ?? emptyKeys;
  const table = useTable({ features, columns, data: rows });

  const total = keys.data?.meta.total ?? 0;
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
          <h1 className="text-lg font-bold">API 密钥</h1>
          <p className="text-xs text-muted">调用接口时使用的凭据，明文只在创建时显示一次</p>
        </div>
      </header>

      {created ? <SecretPanel created={created} onClose={() => setCreated(null)} /> : null}

      <CreateKeyForm
        busy={create.isPending}
        error={create.error}
        onCreated={() => {
          create.reset();
        }}
        onSubmit={(input) => create.mutate(input)}
      />

      <section className="mt-6">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-1 rounded-lg border border-edge bg-surface p-1">
            {(
              [
                { label: '全部', value: undefined },
                { label: '已启用', value: true },
                { label: '已吊销', value: false },
              ] as { label: string; value: EnabledFilter }[]
            ).map((option) => (
              <button
                key={option.label}
                type="button"
                aria-pressed={query.enabled === option.value}
                onClick={() => setFilter(option.value)}
                className={`min-h-9 rounded-md px-3 text-xs ${
                  query.enabled === option.value ? 'bg-subtle text-ink' : 'text-muted'
                }`}
              >
                {option.label}
              </button>
            ))}
          </div>
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

        {keys.isPending ? <TableSkeleton /> : null}

        {keys.isError && !unauthorized ? (
          <ErrorState
            error={keys.error}
            onRetry={() => void keys.refetch()}
            busy={keys.isFetching}
          />
        ) : null}

        {keys.isSuccess && rows.length === 0 ? (
          <EmptyState
            filtered={query.enabled !== undefined}
            onClearFilter={() => setFilter(undefined)}
          />
        ) : null}

        {keys.isSuccess && rows.length > 0 ? (
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

        {keys.isSuccess && total > 0 ? (
          <div className="mt-3 flex items-center justify-between text-xs text-muted">
            <span className="tabular-nums">
              共 {total} 把 · 第 {query.page} / {Math.max(1, Math.ceil(total / query.size))} 页
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

        {revoke.isError ? <ErrorState error={revoke.error} onRetry={() => revoke.reset()} /> : null}
      </section>

      {pendingRevoke ? (
        <ConfirmRevoke
          apiKey={pendingRevoke}
          busy={revoke.isPending}
          onCancel={() => {
            revoke.reset();
            setPendingRevoke(null);
          }}
          onConfirm={() => revoke.mutate(pendingRevoke.id)}
        />
      ) : null}
    </main>
  );
}

/** CreateKeyForm 是创建表单：提交即校验，错误定位到字段。 */
function CreateKeyForm({
  busy,
  error,
  onSubmit,
  onCreated,
}: {
  busy: boolean;
  error: unknown;
  onSubmit: (input: { name: string; expires_at: string | null }) => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [nameError, setNameError] = useState<string | null>(null);
  const [expiresError, setExpiresError] = useState<string | null>(null);

  function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = name.trim();
    if (trimmed === '') {
      setNameError('请填写密钥名');
      return;
    }
    if ([...trimmed].length > 64) {
      setNameError('密钥名不超过 64 个字符');
      return;
    }
    let expires: string | null = null;
    if (expiresAt !== '') {
      const at = new Date(expiresAt).getTime();
      if (Number.isNaN(at)) {
        setExpiresError('过期时间格式非法');
        return;
      }
      if (at <= Date.now()) {
        setExpiresError('过期时间要晚于当前时间');
        return;
      }
      expires = new Date(at).toISOString();
    }
    setNameError(null);
    setExpiresError(null);
    onCreated();
    onSubmit({ name: trimmed, expires_at: expires });
    setName('');
    setExpiresAt('');
  }

  return (
    <form onSubmit={submit} className="mt-5 rounded-2xl border border-edge bg-surface p-5">
      <h2 className="flex items-center gap-2 text-sm font-semibold">
        <Plus size={16} />
        创建密钥
      </h2>
      <div className="mt-3 flex flex-wrap items-start gap-3">
        <label className="min-w-48 flex-1">
          <span className="mb-1.5 block text-xs text-muted">密钥名</span>
          <input
            value={name}
            maxLength={64}
            onChange={(event) => setName(event.currentTarget.value)}
            placeholder="例如：本地开发"
            aria-invalid={nameError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
          {nameError ? <span className="mt-1 block text-xs text-danger">{nameError}</span> : null}
        </label>
        <label className="min-w-48 flex-1">
          <span className="mb-1.5 block text-xs text-muted">过期时间（可空）</span>
          <input
            type="datetime-local"
            value={expiresAt}
            onChange={(event) => setExpiresAt(event.currentTarget.value)}
            aria-invalid={expiresError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
          {expiresError ? (
            <span className="mt-1 block text-xs text-danger">{expiresError}</span>
          ) : null}
        </label>
        <button
          type="submit"
          disabled={busy}
          className="mt-5 min-h-11 rounded-lg bg-action px-5 text-sm font-semibold text-white disabled:opacity-60"
        >
          {busy ? '签发中…' : '创建密钥'}
        </button>
      </div>
      {error instanceof ApiError ? (
        <div className="mt-3">
          <ErrorNote error={error} />
        </div>
      ) : null}
    </form>
  );
}

/** SecretPanel 展示一次性明文；离开页面后无法再取回。 */
function SecretPanel({ created, onClose }: { created: CreatedApiKey; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(created.secret);
      setCopied(true);
      setCopyFailed(false);
    } catch {
      setCopyFailed(true);
    }
  }

  return (
    <section
      role="status"
      className="mt-5 rounded-2xl border border-warn/40 bg-warn/10 p-5 text-sm"
    >
      <h2 className="flex items-center gap-2 font-semibold text-warn">
        <AlertTriangle size={16} />
        明文仅此一次
      </h2>
      <p className="mt-1 text-xs text-muted">
        「{created.name}」已创建。刷新或离开本页后无法再次查看，列表只显示前缀。
      </p>
      <div className="mt-3 flex items-center gap-2">
        <code className="min-w-0 flex-1 truncate rounded-lg border border-edge bg-surface px-3 py-2 font-mono text-xs">
          {created.secret}
        </code>
        <button
          type="button"
          onClick={() => void copy()}
          className="flex min-h-11 items-center gap-1.5 rounded-lg border border-edge bg-surface px-3 text-xs"
        >
          {copied ? <Check size={16} /> : <Copy size={16} />}
          {copied ? '已复制' : '复制'}
        </button>
      </div>
      {copyFailed ? (
        <p className="mt-2 text-xs text-danger">自动复制不可用，请选中上面的明文手动复制。</p>
      ) : null}
      <button
        type="button"
        onClick={onClose}
        className="mt-3 flex min-h-11 items-center gap-1.5 rounded-lg border border-edge bg-surface px-3 text-xs"
      >
        <X size={16} />
        我已保存
      </button>
    </section>
  );
}

/** ConfirmRevoke 是吊销的二次确认：破坏性操作不直接执行。 */
function ConfirmRevoke({
  apiKey,
  busy,
  onCancel,
  onConfirm,
}: {
  apiKey: ApiKey;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <div className="fixed inset-0 flex items-center justify-center bg-ink/40 px-4">
      <div
        role="dialog"
        aria-modal="true"
        aria-label="确认吊销密钥"
        className="w-full max-w-sm rounded-2xl border border-edge bg-surface p-5"
      >
        <h2 className="flex items-center gap-2 text-sm font-semibold">
          <AlertTriangle size={16} className="text-danger" />
          吊销「{apiKey.name}」
        </h2>
        <p className="mt-2 text-xs text-muted">
          使用该密钥的调用会立即失败，且无法恢复。密钥前缀 {apiKey.key_prefix}…
        </p>
        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={onCancel}
            className="min-h-11 rounded-lg border border-edge px-4 text-sm"
          >
            取消
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={onConfirm}
            className="min-h-11 rounded-lg bg-danger px-4 text-sm font-semibold text-white disabled:opacity-60"
          >
            {busy ? '吊销中…' : '确认吊销'}
          </button>
        </div>
      </div>
    </div>
  );
}

/** EmptyState 是统一空态：给出下一步动作而不是空白。 */
function EmptyState({ filtered, onClearFilter }: { filtered: boolean; onClearFilter: () => void }) {
  return (
    <div className="rounded-2xl border border-edge bg-surface p-8 text-center">
      <KeyRound size={24} className="mx-auto text-muted" />
      <p className="mt-3 text-sm">{filtered ? '没有符合条件的密钥' : '还没有 API 密钥'}</p>
      <p className="mt-1 text-xs text-muted">
        {filtered ? '换个筛选条件，或查看全部密钥。' : '密钥用于调用模型接口，创建后请立即保存明文。'}
      </p>
      {filtered ? (
        <button
          type="button"
          onClick={onClearFilter}
          className="mt-3 min-h-11 rounded-lg border border-edge px-4 text-xs"
        >
          查看全部
        </button>
      ) : null}
    </div>
  );
}

/** ErrorState 是列表的错误态：按业务码分支，重试只由用户手动触发。 */
function ErrorState({
  error,
  onRetry,
  busy = false,
}: {
  error: unknown;
  onRetry: () => void;
  busy?: boolean;
}) {
  const code = error instanceof ApiError ? error.code : Code.Internal;
  return (
    <div className="rounded-2xl border border-edge bg-surface p-6">
      {code === Code.Forbidden ? (
        <>
          <p className="text-sm">当前身份无权访问密钥管理。</p>
          <Link
            to="/"
            className="mt-3 inline-flex min-h-11 items-center rounded-lg border border-edge px-4 text-xs"
          >
            返回首页
          </Link>
        </>
      ) : (
        <>
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
        </>
      )}
    </div>
  );
}

/** ErrorNote 显示失败文案：429 与 5xx 走各自的提示。 */
function ErrorNote({ error }: { error: ApiError }) {
  const text =
    error.code === Code.TooManyRequests
      ? `${error.message}（请求过于频繁，请稍后再试）`
      : error.code >= Code.Internal
        ? '服务端错误，请稍后重试。'
        : error.message;
  return (
    <p className="flex items-start gap-2 text-xs text-danger">
      <AlertTriangle size={16} className="shrink-0" />
      {text}
    </p>
  );
}

/** TableSkeleton 是列表加载态：表格形状的骨架。 */
function TableSkeleton() {
  return (
    <div className="overflow-hidden rounded-2xl border border-edge bg-surface">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="border-b border-edge text-left text-xs text-muted">
            <th scope="col" className="px-3 py-2 font-normal">
              名称
            </th>
            <th scope="col" className="px-3 py-2 font-normal">
              状态
            </th>
            <th scope="col" className="px-3 py-2 font-normal">
              操作
            </th>
          </tr>
        </thead>
        <tbody aria-hidden="true">
          {[0, 1, 2, 3, 4].map((row) => (
            <tr key={row} className="border-b border-edge last:border-b-0">
              <td className="px-3 py-3">
                <span className="block h-4 w-32 animate-pulse rounded bg-subtle" />
              </td>
              <td className="px-3 py-3">
                <span className="block h-4 w-16 animate-pulse rounded bg-subtle" />
              </td>
              <td className="px-3 py-3">
                <span className="block h-4 w-12 animate-pulse rounded bg-subtle" />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="sr-only">正在加载密钥列表</p>
    </div>
  );
}

/** NameCell 在窄屏补一行前缀：前缀列在该断点以下不显示。 */
function NameCell({ apiKey }: { apiKey: ApiKey }) {
  return (
    <>
      <span className="font-medium">{apiKey.name}</span>
      <span className="mt-0.5 block font-mono text-xs text-muted md:hidden">
        {apiKey.key_prefix}…
      </span>
    </>
  );
}

/** StatusTag 是状态呈现：颜色与文字成对，不靠颜色单独表意。 */
function StatusTag({ enabled }: { enabled: boolean }) {
  return (
    <span className="flex items-center gap-1.5 text-xs">
      {enabled ? (
        <>
          <Check size={16} className="text-success" />
          <span className="text-success">已启用</span>
        </>
      ) : (
        <>
          <X size={16} className="text-danger" />
          <span className="text-danger">已吊销</span>
        </>
      )}
    </span>
  );
}

/** TimeCell 按本地化格式显示 RFC3339 时间；无值显示业务含义。 */
function TimeCell({ value, empty }: { value: string | null; empty: string }) {
  if (value === null) {
    return <span className="text-xs text-muted">{empty}</span>;
  }
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) {
    return <span className="text-xs text-muted">{empty}</span>;
  }
  return (
    <time dateTime={value} className="text-xs tabular-nums">
      {at.toLocaleString()}
    </time>
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
