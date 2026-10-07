import { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';

import { AuthShell, ErrorBanner, Field, SubmitButton } from '../components/auth';
import { listProviders, oauthAuthorize, signin } from '../lib/auth';
import { ApiError } from '../lib/envelope';
import { safeRedirect } from '../lib/navigation';
import { saveSession } from '../lib/session';

/** 登录页：账号密码登录与第三方入口。 */
export function Login() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const target = safeRedirect(params.get('redirect'));

  const [identifier, setIdentifier] = useState('');
  const [password, setPassword] = useState('');
  const [providers, setProviders] = useState<string[]>([]);
  const [error, setError] = useState<{ code: number; message: string } | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listProviders()
      .then((items) => setProviders(items.map((p) => p.id)))
      .catch(() => setProviders([]));
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const tokens = await signin(identifier, password);
      saveSession(tokens.access_token, tokens.refresh_token);
      navigate(target, { replace: true });
    } catch (err) {
      setError(
        err instanceof ApiError
          ? { code: err.code, message: err.message }
          : { code: 500, message: '网络异常，请稍后重试' },
      );
    } finally {
      setBusy(false);
    }
  }

  async function openProvider(id: string) {
    try {
      const data = await oauthAuthorize(id);
      window.location.assign(data.authorize_url);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? { code: err.code, message: err.message }
          : { code: 500, message: '网络异常，请稍后重试' },
      );
    }
  }

  return (
    <AuthShell
      title="登录 TokenMP"
      subtitle="统一接入主流 AI 模型的 API 网关"
      footer={
        <span>
          还没有账号？{' '}
          <Link className="text-action" to="/signup">
            立即注册
          </Link>
        </span>
      }
    >
      {error ? <ErrorBanner code={error.code} message={error.message} /> : null}
      <form onSubmit={submit}>
        <Field label="账号" value={identifier} onChange={setIdentifier} autoComplete="username" />
        <Field
          label="密码"
          type="password"
          value={password}
          onChange={setPassword}
          autoComplete="current-password"
        />
        <div className="mb-4 text-right">
          <Link className="text-sm text-action" to="/forgot-password">
            忘记密码
          </Link>
        </div>
        <SubmitButton busy={busy}>登录</SubmitButton>
      </form>
      {providers.length > 0 ? (
        <div className="mt-5">
          <div className="mb-3 flex items-center gap-3 text-xs text-muted">
            <span className="h-px flex-1 bg-edge" />
            其他登录方式
            <span className="h-px flex-1 bg-edge" />
          </div>
          <div className="flex gap-3">
            {providers.map((id) => (
              <button
                key={id}
                type="button"
                onClick={() => void openProvider(id)}
                className="min-h-11 flex-1 rounded-lg border border-edge bg-canvas font-medium hover:border-action"
              >
                {id === 'google' ? 'Google' : 'GitHub'}
              </button>
            ))}
          </div>
        </div>
      ) : null}
    </AuthShell>
  );
}
