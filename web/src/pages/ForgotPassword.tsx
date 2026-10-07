import { useState } from 'react';
import { Link } from 'react-router-dom';

import { AuthShell, ErrorBanner, Field, SubmitButton } from '../components/auth';
import { sendOtp } from '../lib/auth';
import { ApiError } from '../lib/envelope';

/** 忘记密码页：发送重置验证码。 */
export function ForgotPassword() {
  const [email, setEmail] = useState('');
  const [error, setError] = useState<{ code: number; message: string } | null>(null);
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await sendOtp(email, 'reset');
      setSent(true);
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

  if (sent) {
    return (
      <AuthShell title="查收验证码" subtitle="如果该邮箱已注册，验证码已经发出">
        <div className="text-center text-sm leading-6 text-muted">
          验证码 5 分钟内有效。
          <br />
          <Link className="mt-2 inline-block text-action" to="/reset-password">
            前往重置密码
          </Link>
        </div>
      </AuthShell>
    );
  }

  return (
    <AuthShell
      title="找回密码"
      subtitle="验证码将发送到你的注册邮箱"
      footer={
        <Link className="text-action" to="/login">
          返回登录
        </Link>
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
        <SubmitButton busy={busy}>发送验证码</SubmitButton>
      </form>
    </AuthShell>
  );
}
