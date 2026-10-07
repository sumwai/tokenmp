import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';

import { Logo } from '../components/auth';
import { session, signout } from '../lib/auth';
import { ApiError, Code } from '../lib/envelope';
import { clearSession } from '../lib/session';
import type { SessionUser } from '../lib/auth';

/** 首页：展示当前会话身份；未登录回登录并携带来源地址。 */
export function Home() {
  const navigate = useNavigate();
  const [user, setUser] = useState<SessionUser | null>(null);

  useEffect(() => {
    session()
      .then(setUser)
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.code === Code.Unauthorized) {
          const from = encodeURIComponent(window.location.pathname);
          navigate(`/login?redirect=${from}`, { replace: true });
          return;
        }
        // 其余错误保留页面，由重试按钮承接。
      });
  }, [navigate]);

  async function logout() {
    try {
      await signout();
    } catch {
      // 登出失败同样清本地会话：令牌已过期时本地清理即可达成效果。
    }
    clearSession();
    navigate('/login', { replace: true });
  }

  if (!user) {
    return (
      <main className="flex min-h-screen items-center justify-center text-sm text-muted">
        正在加载…
      </main>
    );
  }

  return (
    <main className="mx-auto max-w-lg px-4 py-10">
      <div className="flex items-center gap-3">
        <Logo size={36} />
        <div>
          <h1 className="text-lg font-bold">TokenMP</h1>
          <p className="text-xs text-muted">统一接入主流 AI 模型的 API 网关</p>
        </div>
      </div>

      <div className="mt-6 rounded-2xl border border-edge bg-surface p-5">
        <div className="flex items-center justify-between">
          <div>
            <div className="text-lg font-semibold">{user.username}</div>
            <div className="mt-1 text-xs text-muted">
              角色 {user.role} · 登录方式 {user.identities.join('、')}
            </div>
          </div>
          <button
            type="button"
            onClick={() => void logout()}
            className="min-h-10 rounded-lg border border-edge px-4 text-sm text-danger hover:border-danger"
          >
            退出登录
          </button>
        </div>
      </div>

      <div className="mt-4 rounded-2xl border border-edge bg-surface p-5 text-sm text-muted">
        会话有效。密钥、用量与请求日志页面将在此陆续开放。
      </div>
    </main>
  );
}
