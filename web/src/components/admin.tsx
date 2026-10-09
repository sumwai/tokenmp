import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table';
import { Check, RotateCw, X } from 'lucide-react';

import { EmptyState, ErrorPanel, Pager } from './requests';
import { ApiError, Code } from '../lib/envelope';
import {
  adminQueryKey,
  adminQuerySearch,
  parseAdminQuery,
  type AdminList,
  type AdminListQuery,
} from '../lib/admin';
import { loginRedirect } from '../lib/navigation';
import { Forbidden } from '../pages/Forbidden';

/**
 * 管理面清单页的公共部分：三态、表格与分页。
 *
 * 八个清单页的差别只有列定义、过滤条件与取数函数，页面的加载态、错误分支、
 * 无权兜底与分页交互都在这里统一，避免八份近似实现各自漂开。
 *
 * 权限不由前端判定：能进到页面只说明清单里有该入口，服务端仍按身份判定，
 * 越权时页面按 403 呈现（web/AGENTS.md 的「前端隐藏只是体验，不是安全边界」）。
 */

/** ColumnMeta 是列定义声明的窄屏档位：断点以下隐藏次要列。 */
export interface ColumnMeta {
  hideBelow?: 'md' | 'lg';
}

/** AdminColumn 是一列的声明：表头、窄屏档位与单元格渲染。 */
export interface AdminColumn<T> {
  /** id 在列之间唯一，同页多列不得重名。 */
  id: string;
  header: string;
  hideBelow?: 'md' | 'lg';
  cell: (row: T) => ReactNode;
}

/** AdminFilterField 是一个过滤输入：键与契约里的查询参数同名。 */
export interface AdminFilterField {
  key: string;
  label: string;
  placeholder?: string;
  type?: string;
}

/** AdminListPageProps 是清单页的装配参数。 */
export interface AdminListPageProps<T> {
  title: string;
  description: string;
  columns: AdminColumn<T>[];
  load: (query: AdminListQuery) => Promise<AdminList<T>>;
  emptyText: string;
  /** 过滤区；缺省不渲染过滤表单。 */
  filters?: AdminFilterField[];
}

const features = tableFeatures({});

/** buildColumns 把列声明转成表格列定义；窄屏档位随列定义走，页面不写断点分支。 */
function buildColumns<T extends { id: number }>(columns: AdminColumn<T>[]) {
  const helper = createColumnHelper<typeof features, T>();
  return helper.columns(
    columns.map((column) =>
      helper.display({
        id: column.id,
        header: column.header,
        meta: { hideBelow: column.hideBelow } satisfies ColumnMeta,
        cell: (ctx) => column.cell(ctx.row.original),
      }),
    ),
  );
}

/** AdminListPage 是清单页：URL 状态、三态与表格。 */
export function AdminListPage<T extends { id: number }>({
  title,
  description,
  columns,
  load,
  emptyText,
  filters,
}: AdminListPageProps<T>) {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const query = useMemo(() => parseAdminQuery(params), [params]);
  const rows = useMemo<AdminList<T>>(() => ({ items: [], meta: { page: null, size: null, total: null } }), []);

  const list = useQuery({
    queryKey: [title, adminQueryKey(query)],
    queryFn: () => load(query),
    // 翻页与改过滤条件时保留上一份数据，列表不在加载态与内容之间闪动。
    placeholderData: keepPreviousData,
    retry: false,
  });

  const error = list.error instanceof ApiError ? list.error : null;
  const unauthorized = error?.code === Code.Unauthorized;
  const forbidden = error?.code === Code.Forbidden;

  // 会话失效：回登录页并带回当前地址（含过滤与分页），登录后回到原处。
  useEffect(() => {
    if (!unauthorized) {
      return;
    }
    navigate(loginRedirect(`${window.location.pathname}${window.location.search}`), {
      replace: true,
    });
  }, [unauthorized, navigate]);

  const data = list.data ?? rows;
  const table = useTable({
    features,
    columns: useMemo(() => buildColumns(columns), [columns]),
    data: data.items,
  });

  /** applyQuery 把新的列表状态写进 URL；URL 是页面状态的唯一出处。 */
  function applyQuery(next: AdminListQuery) {
    setParams(adminQuerySearch(next));
  }

  if (forbidden) {
    return (
      <main className="mx-auto max-w-5xl px-4 py-6">
        <Forbidden message={error.message} />
      </main>
    );
  }

  return (
    <main className="mx-auto max-w-5xl px-4 py-6">
      <header className="flex items-center gap-3">
        <Link
          to="/"
          aria-label="返回首页"
          className="flex h-11 w-11 items-center justify-center rounded-lg text-muted hover:text-ink"
        >
          ←
        </Link>
        <div>
          <h1 className="text-lg font-bold">{title}</h1>
          <p className="mt-1 text-xs text-muted">{description}</p>
        </div>
      </header>

      {filters && filters.length > 0 ? (
        <AdminFilterBar fields={filters} query={query} onApply={applyQuery} />
      ) : null}

      {list.isPending ? (
        <TableSkeleton columnCount={columns.length} />
      ) : error ? (
        <ErrorPanel error={error} onRetry={() => void list.refetch()} />
      ) : data.items.length === 0 ? (
        <EmptyState
          title="没有符合条件的记录"
          description={emptyText}
          action={
            Object.keys(query.filters).length > 0 ? (
              <button
                type="button"
                onClick={() => applyQuery({ page: 1, size: query.size, filters: {} })}
                className="min-h-10 rounded-lg border border-edge px-4 text-sm"
              >
                清除筛选
              </button>
            ) : (
              <Link to="/" className="min-h-10 rounded-lg border border-edge px-4 py-2 text-sm">
                返回首页
              </Link>
            )
          }
        />
      ) : (
        <>
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
          <Pager
            page={data.meta.page ?? query.page}
            size={data.meta.size ?? query.size}
            total={data.meta.total ?? data.items.length}
            onPage={(page) => applyQuery({ ...query, page })}
          />
        </>
      )}
    </main>
  );
}

/** AdminFilterBar 是过滤区：输入先落在本地状态，提交才写进 URL 并触发请求。 */
function AdminFilterBar({
  fields,
  query,
  onApply,
}: {
  fields: AdminFilterField[];
  query: AdminListQuery;
  onApply: (next: AdminListQuery) => void;
}) {
  const [draft, setDraft] = useState<Record<string, string>>(query.filters);

  // URL 变化（含深链与后退）时把输入框同步回来，避免输入框与结果不一致。
  useEffect(() => {
    setDraft(query.filters);
  }, [query.filters]);

  return (
    <form
      className="mt-4 rounded-2xl border border-edge bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        onApply({ page: 1, size: query.size, filters: draft });
      }}
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {fields.map((field) => (
          <label key={field.key} className="block">
            <span className="mb-1 block text-xs text-muted">{field.label}</span>
            <input
              type={field.type ?? 'text'}
              value={draft[field.key] ?? ''}
              placeholder={field.placeholder}
              onChange={(event) =>
                setDraft({ ...draft, [field.key]: event.currentTarget.value })
              }
              className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
            />
          </label>
        ))}
      </div>
      <div className="mt-4 flex gap-2">
        <button
          type="submit"
          className="min-h-10 rounded-lg bg-action px-4 text-sm font-semibold text-white"
        >
          应用筛选
        </button>
        <button
          type="button"
          onClick={() => onApply({ page: 1, size: query.size, filters: {} })}
          className="min-h-10 rounded-lg border border-edge px-4 text-sm"
        >
          清除筛选
        </button>
      </div>
    </form>
  );
}

/** TableSkeleton 是列表加载态：表格形状的骨架。 */
function TableSkeleton({ columnCount }: { columnCount: number }) {
  const cells = Array.from({ length: Math.min(columnCount, 4) }, (_, index) => index);
  return (
    <div className="mt-4 overflow-hidden rounded-2xl border border-edge bg-surface">
      <table className="w-full border-collapse text-sm">
        <tbody aria-hidden="true">
          {[0, 1, 2, 3, 4].map((row) => (
            <tr key={row} className="border-b border-edge last:border-b-0">
              {cells.map((cell) => (
                <td key={cell} className="px-3 py-3">
                  <span className="block h-4 w-24 animate-pulse rounded bg-subtle" />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <p className="sr-only">正在加载清单</p>
    </div>
  );
}

/** AdminFlag 呈现启用 / 停用：颜色与文字成对，不靠颜色单独表意。 */
export function AdminFlag({ on, onText, offText }: { on: boolean; onText: string; offText: string }) {
  return (
    <span className="flex items-center gap-1.5 text-xs">
      {on ? <Check size={16} className="text-success" /> : <X size={16} className="text-danger" />}
      <span className={on ? 'text-success' : 'text-danger'}>{on ? onText : offText}</span>
    </span>
  );
}

/** AdminText 渲染一个文本取值；空值给占位符。 */
export function AdminText({ value, empty = '—' }: { value: string; empty?: string }) {
  if (value === '') {
    return <span className="text-xs text-muted">{empty}</span>;
  }
  return <span>{value}</span>;
}

/** AdminNumber 渲染一个标识类整数。 */
export function AdminNumber({ value }: { value: number | null }) {
  if (value === null) {
    return <span className="text-xs text-muted">—</span>;
  }
  return <span className="tabular-nums">{value}</span>;
}

/** AdminDecimal 渲染十进制字符串：原样展示，不经 Number 参与计算或格式化。 */
export function AdminDecimal({ value }: { value: string }) {
  return <span className="tabular-nums">{value}</span>;
}

/** AdminTime 渲染 RFC3339 时刻；无取值时给业务含义的占位文案。 */
export function AdminTime({ value, empty }: { value: string | null; empty: string }) {
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

/** RetryNote 是错误态下的手动重试按钮。 */
export function RetryNote({ onRetry }: { onRetry: () => void }) {
  return (
    <button
      type="button"
      onClick={onRetry}
      className="mt-3 flex min-h-11 items-center gap-1.5 rounded-lg border border-edge px-4 text-xs"
    >
      <RotateCw size={16} />
      重试
    </button>
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
