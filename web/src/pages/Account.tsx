import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';

import {
  DEFAULT_RECENT,
  MAX_RECENT,
  QUOTA_SCALE,
  RECENT_SCALE,
  fetchAccount,
  normalizeRecent,
} from '../lib/account';
import type { AccountSummary } from '../lib/account';
import { formatTimestamp } from '../lib/datetime';
import { formatDecimal, formatUnitAmount, parseUnsignedInt } from '../lib/decimal';
import { ApiError, Code } from '../lib/envelope';
import { Forbidden } from './Forbidden';

/**
 * 账户概览页：包存量、窗口限额、最近流水三个分区。
 *
 * 会话、导航与无权访问的兜底在控制台骨架里；本页只渲染账户摘要。
 * 金额与用量都是 decimal 字符串，展示只做补零与千分位（lib/decimal），不经
 * `Number` / `parseFloat`。流水条数写在 URL query，刷新与分享保持同一视图。
 */

/**
 * 结算单位、限额口径与处置方式的中文名。
 *
 * 服务端读取路径对未知取值原样返回（版本回滚时不放大成整批失败），页面沿用同一取舍：
 * 认不出的取值直接露出原值，而不是显示成空白或猜一个近似项。
 */
const UNIT_LABEL: Record<string, string> = {
  currency: '货币额度',
  token: 'token 存量',
  credit: '赠送积分',
};

const FALLBACK_LABEL: Record<string, string> = {
  charge_balance: '包扣尽后转扣余额',
  reject: '包扣尽即拒绝',
};

const SCOPE_LABEL: Record<string, string> = {
  account: '账户',
  api_key: '密钥',
};

const METRIC_LABEL: Record<string, string> = {
  input_token: '输入 token',
  output_token: '输出 token',
  cache_read_token: '缓存读取 token',
  cache_write_token: '缓存写入 token',
  cache_write_5m: '缓存写入 token（5 分钟档）',
  cache_write_1h: '缓存写入 token（1 小时档）',
  reasoning_token: '推理 token',
  request: '请求次数',
};

const WINDOW_LABEL: Record<string, string> = {
  rolling: '滚动窗口',
  calendar: '自然周期',
};

const PERIOD_LABEL: Record<string, string> = {
  '5h': '5 小时',
  day: '日',
  week: '周',
  month: '月',
  total: '总量',
};

const ACTION_LABEL: Record<string, string> = {
  reject: '超限拒绝',
  throttle: '超限限速',
};

/** 流水条数的可选取值；0 表示不取流水。 */
const RECENT_OPTIONS = [0, 10, 20, 50, MAX_RECENT];

/** label 取中文名；未登记的取值原样返回。 */
function label(names: Record<string, string>, key: string): string {
  return names[key] ?? key;
}

type LoadState =
  | { state: 'loading' }
  | { state: 'ready'; summary: AccountSummary }
  | { state: 'failed'; code: number; message: string; retryAfter: number | null };

/** Account 是账户概览页。 */
export function Account() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const rawRecent = params.get('recent');
  const recent = normalizeRecent(rawRecent);
  const [reloadKey, setReloadKey] = useState(0);
  const [load, setLoad] = useState<LoadState>({ state: 'loading' });

  // URL 里的取值可能与契约不一致（如 recent=1000）：收敛成契约取值写回地址，
  // 使地址栏、分享链接与页面展示三者一致。
  useEffect(() => {
    if (rawRecent !== null && rawRecent !== String(recent)) {
      setParams({ recent: String(recent) }, { replace: true });
    }
  }, [rawRecent, recent, setParams]);

  useEffect(() => {
    let cancelled = false;
    setLoad({ state: 'loading' });
    fetchAccount(recent)
      .then((summary) => {
        if (!cancelled) {
          setLoad({ state: 'ready', summary });
        }
      })
      .catch((err: unknown) => {
        if (cancelled) {
          return;
        }
        const code = err instanceof ApiError ? err.code : Code.Internal;
        const message = err instanceof ApiError ? err.message : '网络异常，请稍后重试';
        if (code === Code.Unauthorized) {
          // 会话失效回登录页；回跳地址带本页路径与参数，登录后回到同一视图。
          const from = encodeURIComponent(`${window.location.pathname}${window.location.search}`);
          navigate(`/login?redirect=${from}`, { replace: true });
          return;
        }
        setLoad({
          state: 'failed',
          code,
          message,
          retryAfter: err instanceof ApiError ? err.retryAfter : null,
        });
      });
    return () => {
      cancelled = true;
    };
  }, [recent, reloadKey, navigate]);

  function reload() {
    setReloadKey((key) => key + 1);
  }

  function setRecent(value: number) {
    setParams({ recent: String(value) });
  }

  if (load.state === 'loading') {
    return (
      <div>
        <Title subtitle="包存量、窗口限额与最近流水" />
        <TableSkeleton title="包存量" columns={4} />
        <TableSkeleton title="窗口限额" columns={7} />
        <TableSkeleton title="最近流水" columns={3} />
      </div>
    );
  }

  if (load.state === 'failed') {
    if (load.code === Code.Forbidden) {
      return (
        <div>
          <Title subtitle="包存量、窗口限额与最近流水" />
          <Forbidden message={load.message} />
        </div>
      );
    }
    const title =
      load.code === Code.TooManyRequests
        ? '请求过于频繁'
        : load.code >= 500
          ? '服务暂时不可用'
          : '加载失败';
    const hint =
      load.code === Code.TooManyRequests && load.retryAfter !== null
        ? `请在 ${load.retryAfter} 秒后重试`
        : load.message;
    return (
      <div>
        <Title subtitle="包存量、窗口限额与最近流水" />
        <Section title={title}>
          <div className="flex flex-col items-start gap-3 text-sm">
            <p className="text-muted">{hint}</p>
            <GhostButton onClick={reload}>重新加载</GhostButton>
          </div>
        </Section>
      </div>
    );
  }

  const { summary } = load;
  return (
    <div>
      <Title subtitle={`账户 ${summary.account.code}`} />

      <Section title="包存量" hint="同单位的包各自成条、按扣减顺序排列，第一条是下次结算最先扣的包。">
        {summary.buckets.length === 0 ? (
          <Empty text="当前没有可用的包存量：调用不再有包可扣，按余额或上游配额结算。" />
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-xs text-muted">
                <Th>单位</Th>
                <Th align="right">剩余</Th>
                <Th>包扣尽后</Th>
                <Th align="right">到期</Th>
              </tr>
            </thead>
            <tbody>
              {summary.buckets.map((bucket, index) => (
                <tr key={`${bucket.unit}-${index}`} className="border-t border-edge">
                  <Td>{label(UNIT_LABEL, bucket.unit)}</Td>
                  <Td align="right" numeric>
                    {formatUnitAmount(bucket.unit, bucket.remaining)}
                  </Td>
                  <Td>{label(FALLBACK_LABEL, bucket.fallback)}</Td>
                  <Td align="right">
                    {bucket.expires_at === null ? '不过期' : formatTimestamp(bucket.expires_at)}
                  </Td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section title="窗口限额" hint="账户维度的限额；已用量按当前窗口聚合，重置时间为窗口结束时刻。">
        {summary.quotas.length === 0 ? (
          <Empty text="当前账户没有生效中的窗口限额：调用不受窗口限额约束。" />
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-xs text-muted">
                <Th>维度</Th>
                <Th>指标</Th>
                <Th>窗口</Th>
                <Th align="right">已用</Th>
                <Th align="right">限额</Th>
                <Th>超限处置</Th>
                <Th align="right">重置</Th>
              </tr>
            </thead>
            <tbody>
              {summary.quotas.map((quota, index) => (
                <tr key={`${quota.scope}-${quota.metric}-${index}`} className="border-t border-edge">
                  <Td>{label(SCOPE_LABEL, quota.scope)}</Td>
                  <Td>{label(METRIC_LABEL, quota.metric)}</Td>
                  <Td>
                    {label(PERIOD_LABEL, quota.period)}
                    {quota.period === 'total' ? '' : `（${label(WINDOW_LABEL, quota.window_kind)}）`}
                  </Td>
                  <Td align="right" numeric>
                    {formatDecimal(quota.used, QUOTA_SCALE)}
                  </Td>
                  <Td align="right" numeric>
                    {formatDecimal(quota.limit, QUOTA_SCALE)}
                  </Td>
                  <Td>{label(ACTION_LABEL, quota.action)}</Td>
                  <Td align="right">
                    {quota.resets_at === null ? '不重置' : formatTimestamp(quota.resets_at)}
                  </Td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>

      <Section
        title="最近流水"
        hint="按写入时刻倒序，一次请求一条；条数只影响本页取回多少条。"
        action={
          <label className="flex items-center gap-2 text-xs text-muted">
            显示条数
            <select
              value={String(recent)}
              onChange={(e) => setRecent(parseUnsignedInt(e.currentTarget.value) ?? DEFAULT_RECENT)}
              className="min-h-11 rounded-lg border border-edge bg-surface px-2 text-sm text-ink"
            >
              {RECENT_OPTIONS.map((option) => (
                <option key={option} value={String(option)}>
                  {option === 0 ? '不取流水' : `最近 ${option} 条`}
                </option>
              ))}
            </select>
          </label>
        }
      >
        {summary.recent.length === 0 ? (
          // recent=0 时服务端不返回流水，与「窗口内确实没有流水」是两件事，文案要分开。
          recent === 0 ? (
            <Empty
              text="未请求最近流水。"
              action={<GhostButton onClick={() => setRecent(DEFAULT_RECENT)}>加载最近 {DEFAULT_RECENT} 条</GhostButton>}
            />
          ) : (
            <Empty text="当前窗口内没有调用流水。" />
          )
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-xs text-muted">
                <Th align="right">时间</Th>
                <Th>模型</Th>
                <Th align="right">应扣量</Th>
              </tr>
            </thead>
            <tbody>
              {summary.recent.map((usage, index) => (
                <tr key={`${usage.created_at}-${index}`} className="border-t border-edge">
                  <Td align="right" numeric>
                    {formatTimestamp(usage.created_at)}
                  </Td>
                  <Td>{usage.model}</Td>
                  <Td align="right" numeric>
                    {formatDecimal(usage.charged_amount, RECENT_SCALE)}
                  </Td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </div>
  );
}

/** Title 是页面标题与说明；控制台头部与导航由骨架提供。 */
function Title({ subtitle }: { subtitle: string }) {
  return (
    <div>
      <h1 className="text-lg font-bold">账户概览</h1>
      <p className="mt-1 text-xs text-muted">{subtitle}</p>
    </div>
  );
}

/** Section 是一个分区：标题、可选提示、可选右上角控件。 */
function Section({
  title,
  hint,
  action,
  children,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="mt-4 rounded-2xl border border-edge bg-surface p-5">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 className="text-sm font-semibold">{title}</h2>
          {hint ? <p className="mt-1 text-xs text-muted">{hint}</p> : null}
        </div>
        {action}
      </div>
      <div className="mt-3 overflow-x-auto">{children}</div>
    </section>
  );
}

/** TableSkeleton 是列表视图的加载骨架：表头位留白 + 三行占位。 */
function TableSkeleton({ title, columns }: { title: string; columns: number }) {
  return (
    <Section title={title}>
      <div className="animate-pulse" role="status" aria-label={`${title}正在加载`}>
        {[0, 1, 2].map((row) => (
          <div key={row} className="flex gap-3 py-2">
            {Array.from({ length: columns }, (_, column) => (
              <div key={column} className="h-4 flex-1 rounded bg-subtle" />
            ))}
          </div>
        ))}
      </div>
    </Section>
  );
}

/** Empty 是统一空态：说明当前没有数据，并给出下一步动作。 */
function Empty({ text, action }: { text: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-start gap-3 text-sm text-muted">
      <span>{text}</span>
      {action}
    </div>
  );
}

/** GhostButton 是次级动作按钮；手动重试与空态动作用它。 */
function GhostButton({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="min-h-11 rounded-lg border border-edge px-4 text-sm text-ink hover:border-action"
    >
      {children}
    </button>
  );
}

/** Th 是表头单元格。 */
function Th({ children, align = 'left' }: { children: ReactNode; align?: 'left' | 'right' }) {
  return (
    <th scope="col" className={`py-2 font-normal ${align === 'right' ? 'text-right' : 'text-left'}`}>
      {children}
    </th>
  );
}

/** Td 是数据单元格；numeric 打开等宽数字，使数字列按位对齐。 */
function Td({
  children,
  align = 'left',
  numeric = false,
}: {
  children: ReactNode;
  align?: 'left' | 'right';
  numeric?: boolean;
}) {
  const classes = ['py-2', align === 'right' ? 'text-right' : '', numeric ? 'tabular-nums' : ''];
  return <td className={classes.filter(Boolean).join(' ')}>{children}</td>;
}
