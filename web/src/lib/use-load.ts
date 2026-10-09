import { useEffect, useRef, useState } from 'react';

import { ApiError, Code } from './envelope';

/**
 * 页面数据加载：一次请求的三态（加载 / 数据 / 错误）与手动重试。
 *
 * 刻意不自动重试：web/AGENTS.md 的错误态要求 5xx 由用户手动重试，自动循环重试会把
 * 限流放大成持续压力。key 变化即重新加载，等价于「URL 变了就重新取数」。
 */

/** LoadState 是一次加载的状态。 */
export interface LoadState<T> {
  data: T | null;
  error: ApiError | null;
  loading: boolean;
  reload: () => void;
}

/** useLoad 按 key 加载数据；key 变化重新发起，卸载或 key 变化时丢弃在途结果。 */
export function useLoad<T>(load: () => Promise<T>, key: string): LoadState<T> {
  const [state, setState] = useState<{ data: T | null; error: ApiError | null; loading: boolean }>(
    { data: null, error: null, loading: true },
  );
  const [attempt, setAttempt] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    let cancelled = false;
    setState({ data: null, error: null, loading: true });
    loadRef.current()
      .then((data) => {
        if (!cancelled) {
          setState({ data, error: null, loading: false });
        }
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setState({ data: null, error: toApiError(error), loading: false });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [key, attempt]);

  return { ...state, reload: () => setAttempt((value) => value + 1) };
}

/** toApiError 把任意异常归一为 ApiError：非业务错误按 500 处理。 */
export function toApiError(error: unknown): ApiError {
  if (error instanceof ApiError) {
    return error;
  }
  return new ApiError(Code.Internal, '网络异常，请稍后重试');
}

/** useUnauthorizedRedirect 在会话失效（401）时跳登录页并携带来源地址。 */
export function useUnauthorizedRedirect(error: ApiError | null, navigate: (to: string) => void): void {
  useEffect(() => {
    if (error?.code !== Code.Unauthorized) {
      return;
    }
    const from = encodeURIComponent(`${window.location.pathname}${window.location.search}`);
    navigate(`/login?redirect=${from}`);
  }, [error, navigate]);
}
