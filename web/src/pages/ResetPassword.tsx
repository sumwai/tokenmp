import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';

import { AuthShell, ErrorBanner, Field, SubmitButton } from '../components/auth';
import { resetPassword } from '../lib/auth';
import { ApiError } from '../lib/envelope';

/** 重置密码页：邮箱、验证码、新密码。 */
export function ResetPassword() {
  const navigate = useNavigate();
  const [email, setEmail] = useState('');
  const [otp, setOtp] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<{ code: number; message: string } | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await resetPassword(email, otp, password);
      navigate('/login?reason=password_reset', { replace: true });
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
      title="重置密码"
      subtitle="重置成功后需重新登录"
      footer={
        <span>
          没有验证码？{' '}
          <Link className="text-action" to="/forgot-password">
            重新发送
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
        <Field label="验证码" value={otp} onChange={setOtp} autoComplete="one-time-code" />
        <Field
          label="新密码"
          type="password"
          value={password}
          onChange={setPassword}
          autoComplete="new-password"
        />
        <SubmitButton busy={busy}>重置密码</SubmitButton>
      </form>
    </AuthShell>
  );
}
