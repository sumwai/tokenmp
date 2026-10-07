import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';

import { AuthShell, ErrorBanner, Field, SubmitButton } from '../components/auth';
import { signup } from '../lib/auth';
import { ApiError } from '../lib/envelope';
import { saveSession } from '../lib/session';

/** 注册页：邮箱、用户名、密码，成功直接进入会话。 */
export function Signup() {
  const navigate = useNavigate();
  const [email, setEmail] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<{ code: number; message: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const tokens = await signup(email, username, password);
      saveSession(tokens.access_token, tokens.refresh_token);
      navigate('/', { replace: true });
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

  return (
    <AuthShell
      title="注册账号"
      subtitle="创建账号后即可管理密钥与用量"
      footer={
        <span>
          已有账号？{' '}
          <Link className="text-action" to="/login">
            返回登录
          </Link>
        </span>
      }
    >
      {error ? <ErrorBanner code={error.code} message={error.message} /> : null}
      <form onSubmit={submit}>
        <Field
          label="邮箱"
          type="email"
          value={email}
          onChange={setEmail}
          autoComplete="email"
        />
        <Field
          label="用户名"
          value={username}
          onChange={setUsername}
          autoComplete="username"
        />
        <Field
          label="密码"
          type="password"
          value={password}
          onChange={setPassword}
          autoComplete="new-password"
        />
        <SubmitButton busy={busy}>注册</SubmitButton>
      </form>
    </AuthShell>
  );
}
