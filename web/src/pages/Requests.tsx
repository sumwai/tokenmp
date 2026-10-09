import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';

import {
  EmptyState,
  ErrorPanel,
  ListSkeleton,
  Pager,
  RequestRow,
  SectionCard,
  StatsList,
} from '../components/requests';
import {
  REQUEST_STATUSES,
  STATS_GROUPS,
  STATS_GROUP_LABELS,
  formatCount,
  fromLocalInput,
  hasFilters,
  listQuerySearch,
  listRequests,
  parseListQuery,
  parseStatsGroup,
  requestStats,
  statusLabel,
  toLocalInput,
  type RequestListQuery,
  type RequestStatus,
} from '../lib/requests';
import { useLoad, useUnauthorizedRedirect } from '../lib/use-load';
import { useVirtualRows } from '../lib/virtual';

/**
 * 请求记录列表页。
 *
 * 筛选、分页与聚合维度全部进 URL：刷新与分享保持状态（web/AGENTS.md 的表格规范）。
 * 长列表按固定行高做窗口化渲染，视口外的行不挂载。
 */

/** 行高：与 RequestRow 的高度类一致，窗口计算据此推算区间。 */
const ROW_HEIGHT = 72;

/** 列表容器高度上限：列表页自带滚动容器，窗口计算读它的滚动位置。 */
const LIST_MAX_HEIGHT = '70vh';

/** 列表页。 */
export function Requests() {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  const query = useMemo(() => parseListQuery(searchParams), [searchParams]);
  const statsGroup = parseStatsGroup(searchParams.get('stats_group_by'));
  const search = searchParams.toString();
  const filterKey = [query.since, query.until, query.model, query.apiKeyID].join('|');

  const list = useLoad(() => listRequests(query), search);
  const stats = useLoad(
    () =>
      requestStats({
        since: query.since,
        until: query.until,
        model: query.model,
        apiKeyID: query.apiKeyID,
        groupBy: statsGroup,
      }),
    `${filterKey}#${statsGroup}`,
  );

  useUnauthorizedRedirect(list.error, (to) => navigate(to, { replace: true }));

  /** applyQuery 把新的列表状态写进 URL；URL 是页面状态的唯一出处。 */
  function applyQuery(next: RequestListQuery) {
    setSearchParams(listQuerySearch(next), { replace: false });
  }

  const rows = useVirtualRows(list.data?.items.length ?? 0, ROW_HEIGHT);
  const visibleRows = (list.data?.items ?? []).slice(rows.window.start, rows.window.end);

  return (
    <main className="mx-auto max-w-3xl px-4 py-6">
      <div>
        <h1 className="text-lg font-bold">请求记录</h1>
        <p className="mt-1 text-xs text-muted">
          按请求时刻倒序；重试过的失败尝试不单独成行，条数与实际调用次数一致。
        </p>
      </div>

      <FilterBar
        query={query}
        onApply={(next) => applyQuery({ ...next, page: 1 })}
        onReset={() => setSearchParams(new URLSearchParams())}
      />

      <SectionCard
        title="计数趋势"
        description="聚合只回答计数，单条记录在下方列表里定位。"
      >
        <div className="mb-3 flex gap-2">
          {STATS_GROUPS.map((group) => (
            <button
              key={group}
              type="button"
              onClick={() => {
                const params = listQuerySearch(query);
                if (group === 'day') {
                  params.delete('stats_group_by');
                } else {
                  params.set('stats_group_by', group);
                }
                setSearchParams(params);
              }}
              className={`min-h-9 rounded-lg border px-3 text-xs ${
                group === statsGroup ? 'border-action text-action' : 'border-edge text-muted'
              }`}
            >
              {STATS_GROUP_LABELS[group]}
            </button>
          ))}
        </div>
        {stats.loading ? (
          <p className="text-xs text-muted">正在加载计数…</p>
        ) : stats.error ? (
          <p className="text-xs text-danger">计数加载失败：{stats.error.message}</p>
        ) : (
          <StatsList
            items={stats.data ?? []}
            emptyText="该区间内没有请求计数。"
          />
        )}
      </SectionCard>

      <div className="mt-4">
        {list.loading ? (
          <ListSkeleton />
        ) : list.error ? (
          <ErrorPanel error={list.error} onRetry={list.reload} />
        ) : (list.data?.items.length ?? 0) === 0 ? (
          <EmptyState
            title="没有符合条件的请求记录"
            description={
              hasFilters(query)
                ? '当前筛选条件下没有记录，放宽条件或清除筛选后再看。'
                : '这个账户还没有请求记录；签发密钥并调用一次模型后即可在此查看。'
            }
            action={
              hasFilters(query) ? (
                <button
                  type="button"
                  onClick={() => setSearchParams(new URLSearchParams())}
                  className="min-h-10 rounded-lg border border-edge px-4 text-sm"
                >
                  清除筛选
                </button>
              ) : (
                <Link
                  to="/"
                  className="min-h-10 rounded-lg border border-edge px-4 py-2 text-sm"
                >
                  返回首页
                </Link>
              )
            }
          />
        ) : (
          <>
            <div
              ref={rows.ref}
              className="overflow-auto rounded-2xl border border-edge bg-surface"
              style={{ maxHeight: LIST_MAX_HEIGHT }}
            >
              <div className="relative" style={{ height: rows.window.totalHeight }}>
                <div className="absolute inset-x-0" style={{ top: rows.window.offsetY }}>
                  {visibleRows.map((item) => (
                    <RequestRow key={item.request_id} item={item} />
                  ))}
                </div>
              </div>
            </div>
            <Pager
              page={list.data?.page ?? query.page}
              size={list.data?.size ?? query.size}
              total={list.data?.total ?? 0}
              onPage={(page) => applyQuery({ ...query, page })}
            />
          </>
        )}
      </div>
    </main>
  );
}

/** FilterBar 是筛选区：输入先落在本地状态，点「应用」才写进 URL 并触发请求。 */
function FilterBar({
  query,
  onApply,
  onReset,
}: {
  query: RequestListQuery;
  onApply: (next: RequestListQuery) => void;
  onReset: () => void;
}) {
  const [draft, setDraft] = useState(query);

  // URL 变化（含深链、后退）时把输入框同步回来，避免输入框与结果不一致。
  useEffect(() => {
    setDraft(query);
  }, [query]);

  return (
    <form
      className="mt-4 rounded-2xl border border-edge bg-surface p-4"
      onSubmit={(event) => {
        event.preventDefault();
        onApply({
          ...draft,
          since: fromLocalInput(draft.since),
          until: fromLocalInput(draft.until),
        });
      }}
    >
      <div className="flex flex-wrap gap-2">
        {([['', '全部'], ...REQUEST_STATUSES.map((status) => [status, statusLabel(status)])] as [
          RequestStatus | '',
          string,
        ][]).map(([value, label]) => (
          <button
            key={value || 'all'}
            type="button"
            onClick={() => setDraft({ ...draft, status: value })}
            className={`min-h-9 rounded-lg border px-3 text-xs ${
              draft.status === value ? 'border-action text-action' : 'border-edge text-muted'
            }`}
          >
            {label}
          </button>
        ))}
      </div>

      <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
        <FilterInput
          label="请求标识"
          value={draft.requestID}
          placeholder="精确匹配一条记录"
          onChange={(value) => setDraft({ ...draft, requestID: value })}
        />
        <FilterInput
          label="模型名"
          value={draft.model}
          placeholder="精确匹配，如 gpt-4o-mini"
          onChange={(value) => setDraft({ ...draft, model: value })}
        />
        <FilterInput
          label="密钥 id"
          value={draft.apiKeyID}
          placeholder="正整数"
          onChange={(value) => setDraft({ ...draft, apiKeyID: value })}
        />
        <div className="grid grid-cols-2 gap-2">
          <FilterInput
            label="起始时刻"
            type="datetime-local"
            value={toLocalInput(draft.since)}
            onChange={(value) => setDraft({ ...draft, since: value })}
          />
          <FilterInput
            label="结束时刻"
            type="datetime-local"
            value={toLocalInput(draft.until)}
            onChange={(value) => setDraft({ ...draft, until: value })}
          />
        </div>
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
          onClick={onReset}
          className="min-h-10 rounded-lg border border-edge px-4 text-sm"
        >
          清除筛选
        </button>
        <span className="ml-auto self-center text-xs text-muted tabular-nums">
          每页 {formatCount(query.size)} 条
        </span>
      </div>
    </form>
  );
}

/** FilterInput 是筛选输入框。 */
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
