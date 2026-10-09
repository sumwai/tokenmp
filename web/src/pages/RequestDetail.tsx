import { ArrowLeft } from 'lucide-react';
import { Link, useNavigate, useParams } from 'react-router-dom';

import {
  AttemptTimeline,
  EmptyState,
  ErrorPanel,
  Field,
  FieldGrid,
  SectionCard,
  ShapeDiff,
  ShapePreview,
  StatusBadge,
  UsageSummary,
} from '../components/requests';
import { ApiError, Code } from '../lib/envelope';
import {
  formatBytes,
  formatDuration,
  formatMoment,
  getRequestDetail,
  payloadNotice,
  requestsPath,
  EMPTY_LIST_QUERY,
  type RequestDetail as RequestDetailData,
} from '../lib/requests';
import { useLoad, useUnauthorizedRedirect } from '../lib/use-load';

/**
 * 请求记录详情页。
 *
 * 记录标识来自路径，因此深链可分享、刷新保持（验收条件 2）。报文缺失态由
 * `payload_available` 区分，显示为记录口径而不是错误（验收条件 3）。
 */
export function RequestDetail() {
  const { requestID = '' } = useParams<{ requestID: string }>();
  const navigate = useNavigate();
  const detail = useLoad(() => getRequestDetail(requestID), requestID);

  useUnauthorizedRedirect(detail.error, (to) => navigate(to, { replace: true }));

  const backLink = (
    <Link to={requestsPath(EMPTY_LIST_QUERY)} className="text-sm text-action">
      返回列表
    </Link>
  );

  return (
    <main className="mx-auto max-w-3xl px-4 py-6">
      <div className="flex items-center gap-3">
        <Link to={requestsPath(EMPTY_LIST_QUERY)} aria-label="返回列表">
          <ArrowLeft size={20} aria-hidden />
        </Link>
        <h1 className="text-lg font-bold">请求详情</h1>
      </div>
      <p className="mt-1 truncate font-mono text-xs text-muted">{requestID}</p>

      {detail.loading ? (
        <p className="mt-6 text-sm text-muted">正在加载…</p>
      ) : detail.error ? (
        detail.error.code === Code.NotFound ? (
          <div className="mt-4">
            <EmptyState
              title="请求记录不存在"
              description="该标识不存在，或不属于当前账户。请从列表进入。"
              action={backLink}
            />
          </div>
        ) : (
          <div className="mt-4">
            <ErrorPanel error={detail.error} onRetry={detail.reload} />
          </div>
        )
      ) : detail.data ? (
        <DetailBody detail={detail.data} />
      ) : (
        <div className="mt-4">
          <ErrorPanel
            error={new ApiError(Code.Internal, '服务端错误')}
            onRetry={detail.reload}
          />
        </div>
      )}
    </main>
  );
}

/** DetailBody 渲染一条记录的完整视图。 */
function DetailBody({ detail }: { detail: RequestDetailData }) {
  const item = detail.request;
  return (
    <>
      <SectionCard title="摘要">
        <div className="flex items-center gap-2">
          <StatusBadge status={item.status} />
          <span className="text-sm font-medium">{item.model}</span>
          {item.upstream_model !== item.model ? (
            <span className="text-xs text-muted">上游模型 {item.upstream_model}</span>
          ) : null}
        </div>
        <div className="mt-3">
          <FieldGrid>
            <Field label="请求时刻">{formatMoment(item.created_at)}</Field>
            <Field label="耗时">{formatDuration(item.duration_ms)}</Field>
            <Field label="返回状态码">HTTP {item.http_status}</Field>
            <Field label="上游状态码">{item.upstream_status ?? '未取得'}</Field>
            <Field label="失败分类">{item.failure_class ?? '无'}</Field>
            <Field label="错误码">{item.error_code ?? '无'}</Field>
            <Field label="客户端协议">{item.protocol}</Field>
            <Field label="上游协议">
              {item.upstream_protocol}
              {item.cross_protocol ? '（跨协议重建）' : ''}
            </Field>
            <Field label="密钥 id">{item.api_key_id}</Field>
            <Field label="流式">{item.stream ? '是' : '否'}</Field>
            <Field label="已写字节数">{formatBytes(item.written_bytes ?? 0)}</Field>
            <Field label="报文">{item.payload_available ? '有' : '无'}</Field>
          </FieldGrid>
        </div>
      </SectionCard>

      <SectionCard title="用量" description="子项包含于主计数：缓存读写属于输入，推理属于输出。">
        <UsageSummary item={item} />
      </SectionCard>

      <SectionCard title="尝试时间线" description="重试、渠道回退各占一条，按尝试序号排列。">
        <AttemptTimeline attempts={detail.attempts} />
      </SectionCard>

      <SectionCard
        title="报文对比"
        description="客户端请求结构与实际发往上游的结构：两者的差异即网关改写的部分。报文按落盘即脱敏处理，不含用户内容。"
      >
        <ShapeDiff
          requestShape={detail.request_shape}
          upstreamShape={detail.upstream_request_shape}
          rewrittenParts={detail.rewritten_parts}
          payloadAvailable={item.payload_available}
        />
      </SectionCard>

      <SectionCard title="客户端请求结构" description="键名与嵌套层次原样保留，用户内容替换为类型标记。">
        {item.payload_available ? (
          <ShapePreview value={detail.request_shape} />
        ) : (
          <p className="text-sm text-muted">{payloadNotice(false)}</p>
        )}
      </SectionCard>

      <SectionCard title="错误响应结构" description="仅失败请求保留；成功请求与超期后为空。">
        {detail.error_response_shape === null ? (
          <p className="text-sm text-muted">本次没有错误响应报文。</p>
        ) : (
          <ShapePreview value={detail.error_response_shape} />
        )}
      </SectionCard>

      <SectionCard
        title="客户端线索"
        description="取自连接层与客户端自报，可被伪造，只作排障线索。"
      >
        <FieldGrid>
          <Field label="客户端地址">{detail.client_ip || '未记录'}</Field>
          <Field label="User-Agent">{detail.user_agent || '未自报'}</Field>
        </FieldGrid>
      </SectionCard>
    </>
  );
}
