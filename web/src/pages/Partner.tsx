import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, ArrowLeft, Check, Copy, Plus, RotateCw, Server, X } from 'lucide-react';

import { ApiError, Code } from '../lib/envelope';
import {
  createChannel,
  createCredential,
  listChannels,
  listCredentials,
  setChannelEnabled,
  setCredentialEnabled,
  type CreatePartnerChannelInput,
  type CreatedPartnerCredential,
  type PartnerChannel,
  type PartnerCredential,
} from '../lib/partner';
import {
  DEFAULT_PAGE_SIZE,
  MAX_PAGE_SIZE,
  accountsQueryString,
  parseAccountsQuery,
  type AccountsQuery,
} from '../lib/partnerUrl';
import { loginRedirect } from '../lib/navigation';

/**
 * 商家域首页：上游账号（渠道与凭据）的列表、登记与启停。
 *
 * 作用域是会话推导出的商家，页面不提供商家切换；清单里没有商家入口的身份看不到本页。
 */

/** 每页条数可选项，上限与契约 Size 的 maximum 一致。 */
const sizeOptions = [DEFAULT_PAGE_SIZE, 50, MAX_PAGE_SIZE];

/** 协议方言的取值与展示名，与契约 PartnerChannel.type 的枚举一致。 */
const channelTypes = [
  { value: 'openai_chat', label: 'OpenAI Chat' },
  { value: 'openai_responses', label: 'OpenAI Responses' },
  { value: 'anthropic_messages', label: 'Anthropic Messages' },
  { value: 'gemini_generate', label: 'Gemini Generate' },
] as const;

/** 凭据注入形态的取值与展示名，与契约 credential_style 的枚举一致。 */
const credentialStyles = [
  { value: '', label: '按协议现状' },
  { value: 'authorization', label: 'Authorization 头' },
  { value: 'x-api-key', label: 'x-api-key 头' },
  { value: 'x-goog-api-key', label: 'x-goog-api-key 头' },
  { value: 'query', label: '查询参数' },
] as const;

type EnabledFilter = boolean | undefined;

export function Partner() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const queryClient = useQueryClient();
  const query = useMemo(() => parseAccountsQuery(params.toString()), [params]);

  const [createdCredential, setCreatedCredential] = useState<CreatedPartnerCredential | null>(null);

  const channels = useQuery({
    queryKey: ['partner-channels', query.page, query.size, query.enabled ?? 'all'],
    queryFn: () => listChannels(query),
    placeholderData: keepPreviousData,
    retry: false,
  });
  const credentials = useQuery({
    queryKey: ['partner-credentials', query.page, query.size],
    queryFn: () => listCredentials(query),
    placeholderData: keepPreviousData,
    retry: false,
  });

  const unauthorized =
    (channels.error instanceof ApiError && channels.error.code === Code.Unauthorized) ||
    (credentials.error instanceof ApiError && credentials.error.code === Code.Unauthorized);

  // 会话失效：回登录页并带回当前地址（含筛选与分页），登录后回到原处。
  useEffect(() => {
    if (!unauthorized) return;
    navigate(loginRedirect(`${window.location.pathname}${window.location.search}`), {
      replace: true,
    });
  }, [unauthorized, navigate]);

  const addChannel = useMutation({
    mutationFn: createChannel,
    retry: false,
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['partner-channels'] }),
  });
  const addCredential = useMutation({
    mutationFn: createCredential,
    retry: false,
    onSuccess: (data) => {
      setCreatedCredential(data);
      void queryClient.invalidateQueries({ queryKey: ['partner-credentials'] });
    },
  });
  const toggleChannel = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) => setChannelEnabled(id, enabled),
    retry: false,
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['partner-channels'] }),
  });
  const toggleCredential = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) =>
      setCredentialEnabled(id, enabled),
    retry: false,
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['partner-credentials'] }),
  });

  /** applyQuery 把新条件写进 URL：条件全在 query 上，刷新与分享都能复原。 */
  function applyQuery(next: AccountsQuery) {
    setParams(new URLSearchParams(accountsQueryString(next)));
  }

  const channelRows = channels.data?.items ?? [];
  const credentialRows = credentials.data?.items ?? [];
  const channelTotal = channels.data?.meta.total ?? 0;
  const credentialTotal = credentials.data?.meta.total ?? 0;

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
        <div className="min-w-0 flex-1">
          <h1 className="text-lg font-bold">商家</h1>
          <p className="text-xs text-muted">上游账号的登记与启停，凭据明文只在创建时显示一次</p>
        </div>
        <Link
          to="/partner/usage"
          className="flex min-h-11 shrink-0 items-center rounded-lg border border-edge px-3 text-xs"
        >
          名下调用量
        </Link>
      </header>

      {createdCredential ? (
        <SecretPanel created={createdCredential} onClose={() => setCreatedCredential(null)} />
      ) : null}

      <CreateChannelForm
        busy={addChannel.isPending}
        error={addChannel.error}
        onSubmit={(input) => {
          addChannel.reset();
          addChannel.mutate(input);
        }}
      />

      <section className="mt-6">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-sm font-semibold">上游渠道</h2>
          <div className="flex items-center gap-1 rounded-lg border border-edge bg-surface p-1">
            {(
              [
                { label: '全部', value: undefined },
                { label: '已启用', value: true },
                { label: '已停用', value: false },
              ] as { label: string; value: EnabledFilter }[]
            ).map((option) => (
              <button
                key={option.label}
                type="button"
                aria-pressed={query.enabled === option.value}
                onClick={() => applyQuery({ page: 1, size: query.size, enabled: option.value })}
                className={`min-h-9 rounded-md px-3 text-xs ${
                  query.enabled === option.value ? 'bg-subtle text-ink' : 'text-muted'
                }`}
              >
                {option.label}
              </button>
            ))}
          </div>
        </div>

        {channels.isPending ? <TableSkeleton rows={3} /> : null}
        {channels.isError && !unauthorized ? (
          <ErrorState error={channels.error} onRetry={() => void channels.refetch()} busy={channels.isFetching} />
        ) : null}
        {channels.isSuccess && channelRows.length === 0 ? (
          <EmptyState
            icon={<Server size={24} className="mx-auto text-muted" />}
            title={query.enabled === undefined ? '还没有登记上游渠道' : '没有符合条件的渠道'}
            hint={
              query.enabled === undefined
                ? '登记一条上游渠道并绑定凭据分组后，调用量会记到本商家名下。'
                : '换个筛选条件，或查看全部渠道。'
            }
          />
        ) : null}
        {channels.isSuccess && channelRows.length > 0 ? (
          <div className="overflow-hidden rounded-2xl border border-edge bg-surface">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-edge text-left text-xs text-muted">
                  <th scope="col" className="px-3 py-2 font-normal">
                    渠道
                  </th>
                  <th scope="col" className="hidden px-3 py-2 font-normal md:table-cell">
                    协议
                  </th>
                  <th scope="col" className="hidden px-3 py-2 font-normal lg:table-cell">
                    上游地址
                  </th>
                  <th scope="col" className="px-3 py-2 font-normal">
                    状态
                  </th>
                  <th scope="col" className="px-3 py-2 font-normal">
                    操作
                  </th>
                </tr>
              </thead>
              <tbody>
                {channelRows.map((channel) => (
                  <ChannelRow
                    key={channel.id}
                    channel={channel}
                    busy={toggleChannel.isPending}
                    onToggle={(enabled) => toggleChannel.mutate({ id: channel.id, enabled })}
                  />
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
        {toggleChannel.isError ? (
          <div className="mt-3">
            <ErrorNote error={asApiError(toggleChannel.error)} />
          </div>
        ) : null}

        <CreateCredentialForm
          busy={addCredential.isPending}
          error={addCredential.error}
          onSubmit={(input) => {
            addCredential.reset();
            addCredential.mutate(input);
          }}
        />

        <div className="mb-3 mt-6 flex items-center justify-between">
          <h2 className="text-sm font-semibold">上游凭据</h2>
          <label className="flex items-center gap-2 text-xs text-muted">
            每页
            <select
              value={query.size}
              onChange={(event) =>
                applyQuery({ page: 1, size: Number(event.currentTarget.value), enabled: query.enabled })
              }
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

        {credentials.isPending ? <TableSkeleton rows={2} /> : null}
        {credentials.isError && !unauthorized ? (
          <ErrorState
            error={credentials.error}
            onRetry={() => void credentials.refetch()}
            busy={credentials.isFetching}
          />
        ) : null}
        {credentials.isSuccess && credentialRows.length === 0 ? (
          <EmptyState
            icon={<Server size={24} className="mx-auto text-muted" />}
            title="还没有登记上游凭据"
            hint="凭据按分组与渠道关联：同分组的渠道共享一份凭据。"
          />
        ) : null}
        {credentials.isSuccess && credentialRows.length > 0 ? (
          <div className="overflow-hidden rounded-2xl border border-edge bg-surface">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-edge text-left text-xs text-muted">
                  <th scope="col" className="px-3 py-2 font-normal">
                    凭据
                  </th>
                  <th scope="col" className="hidden px-3 py-2 font-normal md:table-cell">
                    分组
                  </th>
                  <th scope="col" className="px-3 py-2 font-normal">
                    前缀
                  </th>
                  <th scope="col" className="px-3 py-2 font-normal">
                    状态
                  </th>
                  <th scope="col" className="px-3 py-2 font-normal">
                    操作
                  </th>
                </tr>
              </thead>
              <tbody>
                {credentialRows.map((credential) => (
                  <CredentialRow
                    key={credential.id}
                    credential={credential}
                    busy={toggleCredential.isPending}
                    onToggle={(enabled) => toggleCredential.mutate({ id: credential.id, enabled })}
                  />
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
        {toggleCredential.isError ? (
          <div className="mt-3">
            <ErrorNote error={asApiError(toggleCredential.error)} />
          </div>
        ) : null}

        {channelTotal > 0 || credentialTotal > 0 ? (
          <div className="mt-3 flex items-center justify-between text-xs text-muted">
            <span className="tabular-nums">
              渠道 {channelTotal} 条 · 凭据 {credentialTotal} 条
            </span>
            <div className="flex gap-2">
              <button
                type="button"
                disabled={query.page <= 1}
                onClick={() => applyQuery({ ...query, page: query.page - 1 })}
                className="min-h-9 rounded-lg border border-edge px-3 disabled:opacity-40"
              >
                上一页
              </button>
              <button
                type="button"
                disabled={query.page * query.size >= Math.max(channelTotal, credentialTotal)}
                onClick={() => applyQuery({ ...query, page: query.page + 1 })}
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

/** ChannelRow 是一条渠道：状态与启停动作成对呈现。 */
function ChannelRow({
  channel,
  busy,
  onToggle,
}: {
  channel: PartnerChannel;
  busy: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  return (
    <tr className="border-b border-edge last:border-b-0">
      <td className="px-3 py-3 align-top">
        <span className="font-medium">{channel.name}</span>
        <span className="mt-0.5 block text-xs text-muted">{channel.vendor === '' ? '未标注厂商' : channel.vendor}</span>
        <span className="mt-0.5 block text-xs text-muted md:hidden">
          {channel.type} · {channel.cred_group}
        </span>
      </td>
      <td className="hidden px-3 py-3 align-top text-xs text-muted md:table-cell">
        {channel.type}
        <span className="mt-0.5 block">分组 {channel.cred_group}</span>
      </td>
      <td className="hidden px-3 py-3 align-top font-mono text-xs text-muted lg:table-cell">
        {channel.base_url}
      </td>
      <td className="px-3 py-3 align-top">
        <StatusTag enabled={channel.enabled} />
      </td>
      <td className="px-3 py-3 align-top">
        <button
          type="button"
          disabled={busy}
          onClick={() => onToggle(!channel.enabled)}
          className="min-h-9 rounded-lg border border-edge px-3 text-xs disabled:opacity-40"
        >
          {channel.enabled ? '停用' : '启用'}
        </button>
      </td>
    </tr>
  );
}

/** CredentialRow 是一份凭据：只展示前缀，明文从不出现。 */
function CredentialRow({
  credential,
  busy,
  onToggle,
}: {
  credential: PartnerCredential;
  busy: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  return (
    <tr className="border-b border-edge last:border-b-0">
      <td className="px-3 py-3 align-top">
        <span className="font-medium">{credential.name}</span>
      </td>
      <td className="hidden px-3 py-3 align-top text-xs text-muted md:table-cell">
        {credential.cred_group}
      </td>
      <td className="px-3 py-3 align-top">
        <code className="font-mono text-xs text-muted">{credential.prefix}</code>
      </td>
      <td className="px-3 py-3 align-top">
        <StatusTag enabled={credential.enabled} />
      </td>
      <td className="px-3 py-3 align-top">
        <button
          type="button"
          disabled={busy}
          onClick={() => onToggle(!credential.enabled)}
          className="min-h-9 rounded-lg border border-edge px-3 text-xs disabled:opacity-40"
        >
          {credential.enabled ? '停用' : '启用'}
        </button>
      </td>
    </tr>
  );
}

/** CreateChannelForm 是登记表单：提交即校验，错误定位到字段。 */
function CreateChannelForm({
  busy,
  error,
  onSubmit,
}: {
  busy: boolean;
  error: unknown;
  onSubmit: (input: CreatePartnerChannelInput) => void;
}) {
  const [name, setName] = useState('');
  const [channelType, setChannelType] = useState<string>(channelTypes[0].value);
  const [credGroup, setCredGroup] = useState('');
  const [baseURL, setBaseURL] = useState('');
  const [credentialStyle, setCredentialStyle] = useState<string>('');
  const [fieldError, setFieldError] = useState<string | null>(null);

  function submit(event: FormEvent) {
    event.preventDefault();
    const trimmedName = name.trim();
    const trimmedGroup = credGroup.trim();
    const trimmedURL = baseURL.trim();
    if (trimmedName === '' || [...trimmedName].length > 64) {
      setFieldError('渠道名必填且不超过 64 个字符');
      return;
    }
    if (trimmedGroup === '' || [...trimmedGroup].length > 64) {
      setFieldError('凭据分组必填且不超过 64 个字符');
      return;
    }
    if (trimmedURL === '') {
      setFieldError('上游地址必填');
      return;
    }
    setFieldError(null);
    onSubmit({
      name: trimmedName,
      type: channelType as CreatePartnerChannelInput['type'],
      cred_group: trimmedGroup,
      base_url: trimmedURL,
      ...(credentialStyle === ''
        ? {}
        : { credential_style: credentialStyle as NonNullable<CreatePartnerChannelInput['credential_style']> }),
    });
    setName('');
    setCredGroup('');
    setBaseURL('');
  }

  return (
    <form onSubmit={submit} className="mt-5 rounded-2xl border border-edge bg-surface p-5">
      <h2 className="flex items-center gap-2 text-sm font-semibold">
        <Plus size={16} />
        登记上游渠道
      </h2>
      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        <Field label="渠道名">
          <input
            value={name}
            maxLength={64}
            onChange={(event) => setName(event.currentTarget.value)}
            placeholder="例如：upstream-chat"
            aria-invalid={fieldError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </Field>
        <Field label="协议方言">
          <select
            value={channelType}
            onChange={(event) => setChannelType(event.currentTarget.value)}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm text-ink"
          >
            {channelTypes.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="凭据分组">
          <input
            value={credGroup}
            maxLength={64}
            onChange={(event) => setCredGroup(event.currentTarget.value)}
            placeholder="例如：grp-chat"
            aria-invalid={fieldError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </Field>
        <Field label="上游地址">
          <input
            value={baseURL}
            onChange={(event) => setBaseURL(event.currentTarget.value)}
            placeholder="https://upstream.example"
            aria-invalid={fieldError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </Field>
        <Field label="凭据注入形态">
          <select
            value={credentialStyle}
            onChange={(event) => setCredentialStyle(event.currentTarget.value)}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm text-ink"
          >
            {credentialStyles.map((option) => (
              <option key={option.label} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </Field>
      </div>
      {fieldError ? <p className="mt-2 text-xs text-danger">{fieldError}</p> : null}
      {error instanceof ApiError ? (
        <div className="mt-3">
          <ErrorNote error={error} />
        </div>
      ) : null}
      <button
        type="submit"
        disabled={busy}
        className="mt-4 min-h-11 rounded-lg bg-action px-5 text-sm font-semibold text-white disabled:opacity-60"
      >
        {busy ? '登记中…' : '登记渠道'}
      </button>
    </form>
  );
}

/** CreateCredentialForm 是凭据登记表单：明文只经这一条路径进入。 */
function CreateCredentialForm({
  busy,
  error,
  onSubmit,
}: {
  busy: boolean;
  error: unknown;
  onSubmit: (input: { cred_group: string; name: string; api_key: string }) => void;
}) {
  const [credGroup, setCredGroup] = useState('');
  const [name, setName] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [fieldError, setFieldError] = useState<string | null>(null);

  function submit(event: FormEvent) {
    event.preventDefault();
    const trimmedGroup = credGroup.trim();
    const trimmedName = name.trim();
    if (trimmedGroup === '' || [...trimmedGroup].length > 64) {
      setFieldError('凭据分组必填且不超过 64 个字符');
      return;
    }
    if (trimmedName === '' || [...trimmedName].length > 64) {
      setFieldError('凭据名必填且不超过 64 个字符');
      return;
    }
    if (apiKey.trim() === '') {
      setFieldError('凭据明文必填');
      return;
    }
    setFieldError(null);
    onSubmit({ cred_group: trimmedGroup, name: trimmedName, api_key: apiKey.trim() });
    setCredGroup('');
    setName('');
    setApiKey('');
  }

  return (
    <form onSubmit={submit} className="mt-5 rounded-2xl border border-edge bg-surface p-5">
      <h2 className="flex items-center gap-2 text-sm font-semibold">
        <Plus size={16} />
        登记上游凭据
      </h2>
      <div className="mt-3 grid gap-3 sm:grid-cols-3">
        <Field label="凭据分组">
          <input
            value={credGroup}
            maxLength={64}
            onChange={(event) => setCredGroup(event.currentTarget.value)}
            placeholder="与渠道一致"
            aria-invalid={fieldError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </Field>
        <Field label="凭据名">
          <input
            value={name}
            maxLength={64}
            onChange={(event) => setName(event.currentTarget.value)}
            placeholder="例如：primary"
            aria-invalid={fieldError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </Field>
        <Field label="凭据明文">
          <input
            type="password"
            value={apiKey}
            onChange={(event) => setApiKey(event.currentTarget.value)}
            placeholder="上游签发的密钥"
            aria-invalid={fieldError !== null}
            className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-sm outline-none focus:border-action"
          />
        </Field>
      </div>
      {fieldError ? <p className="mt-2 text-xs text-danger">{fieldError}</p> : null}
      {error instanceof ApiError ? (
        <div className="mt-3">
          <ErrorNote error={error} />
        </div>
      ) : null}
      <button
        type="submit"
        disabled={busy}
        className="mt-4 min-h-11 rounded-lg bg-action px-5 text-sm font-semibold text-white disabled:opacity-60"
      >
        {busy ? '登记中…' : '登记凭据'}
      </button>
    </form>
  );
}

/** SecretPanel 展示一次性明文；离开页面后无法再取回。 */
function SecretPanel({
  created,
  onClose,
}: {
  created: CreatedPartnerCredential;
  onClose: () => void;
}) {
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
    <section role="status" className="mt-5 rounded-2xl border border-warn/40 bg-warn/10 p-5 text-sm">
      <h2 className="flex items-center gap-2 font-semibold text-warn">
        <AlertTriangle size={16} />
        明文仅此一次
      </h2>
      <p className="mt-1 text-xs text-muted">
        「{created.name}」已登记。刷新或离开本页后无法再次查看，列表只显示前缀。
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

/** Field 是表单字段的标签与控件容器。 */
function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-xs text-muted">{label}</span>
      {children}
    </label>
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
          <span className="text-danger">已停用</span>
        </>
      )}
    </span>
  );
}

/** EmptyState 是统一空态：给出下一步动作而不是空白。 */
function EmptyState({ icon, title, hint }: { icon: React.ReactNode; title: string; hint: string }) {
  return (
    <div className="rounded-2xl border border-edge bg-surface p-8 text-center">
      {icon}
      <p className="mt-3 text-sm">{title}</p>
      <p className="mt-1 text-xs text-muted">{hint}</p>
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
          <ErrorNote error={asApiError(error)} />
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

/** TableSkeleton 是列表加载态：表格形状的骨架。 */
function TableSkeleton({ rows }: { rows: number }) {
  return (
    <div className="overflow-hidden rounded-2xl border border-edge bg-surface">
      <table className="w-full border-collapse text-sm">
        <tbody aria-hidden="true">
          {Array.from({ length: rows }, (_, row) => (
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
      <p className="sr-only">正在加载商家数据</p>
    </div>
  );
}

/** asApiError 把任意异常归一为 ApiError：非业务错误按 500 处理。 */
function asApiError(error: unknown): ApiError {
  return error instanceof ApiError ? error : new ApiError(Code.InternalError, '服务端错误');
}
