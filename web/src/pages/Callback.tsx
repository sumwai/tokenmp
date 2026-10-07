import { useEffect, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';

import { oauthExchange } from '../lib/auth';
import { ApiError } from '../lib/envelope';
import { clearOauthProvider, oauthProvider, saveSession } from '../lib/session';

/**
 * 第三方授权回调页：落在 redirect_uri（本机开发为 /auth/callback）上，
 * 提取 code 与 state 换取会话后进入首页。
 *
 * 提供方由发起授权时的记录给出，与服务端 state 绑定的提供方双重校验。
 * 整个流程只会发起一次：开发模式的 StrictMode 会重复执行 effect，
 * 而 state 在服务端是一次性的，重复提交必然第二次失败。
 */
export function Callback() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const startedRef = useRef(false);

  useEffect(() => {
    if (startedRef.current) {
      return;
    }
    startedRef.current = true;

    const code = params.get('code');
    const state = params.get('state');
    const provider = oauthProvider();
    if (!code || !state || !provider) {
      setError('授权信息不完整，请返回登录页重试');
      return;
    }
    oauthExchange(provider, code, state)
      .then((tokens) => {
        clearOauthProvider();
        saveSession(tokens.access_token, tokens.refresh_token);
        navigate('/', { replace: true });
      })
      .catch((err: unknown) => {
        setError(err instanceof ApiError ? err.message : '网络异常，请稍后重试');
      });
  }, [navigate, params]);

  if (error) {
    return (
      <main className="flex min-h-screen flex-col items-center justify-center gap-3 px-4">
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
        <a className="text-sm text-action" href="/login">
          返回登录
        </a>
      </main>
    );
  }

  return (
    <main className="flex min-h-screen items-center justify-center text-sm text-muted">
      正在完成登录…
    </main>
  );
}
