import { useEffect, useRef, useState } from 'react';
import {
  ChartLine,
  Gauge,
  HandCoins,
  House,
  KeyRound,
  Landmark,
  Receipt,
  Route,
  Scale,
  ScrollText,
  ShieldCheck,
  Shuffle,
  Store,
  Tag,
  UserRound,
  Users,
  Wallet,
} from 'lucide-react';
import type { LucideIcon } from 'lucide-react';
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';

import { Logo } from './auth';
import { session, signout } from '../lib/auth';
import type { SessionUser } from '../lib/auth';
import { allows, entryForPath, loadConsole } from '../lib/console';
import type { ConsoleData } from '../lib/console';
import { ApiError, Code } from '../lib/envelope';
import { clearSession } from '../lib/session';
import { Forbidden } from '../pages/Forbidden';

/**
 * 控制台骨架：会话校验与登录回跳、按服务端清单渲染的导航、无权访问的兜底。
 *
 * 页面由子路由提供：骨架不认识任何角色，也不为任何角色分叉 —— 能看见什么由清单
 * 与能力集合决定。
 */

/** 清单里的图标名 → Lucide 组件。未登记的名字不渲染图标，其余照常渲染。 */
const icons: Record<string, LucideIcon> = {
  house: House,
  'key-round': KeyRound,
  'chart-line': ChartLine,
  'scroll-text': ScrollText,
  'user-round': UserRound,
  // 管理面条目的图标（清单里的名字由服务端下发，这里只做名字到组件的映射）。
  route: Route,
  'shield-check': ShieldCheck,
  shuffle: Shuffle,
  users: Users,
  tag: Tag,
  gauge: Gauge,
  scale: Scale,
  receipt: Receipt,
  store: Store,
  'hand-coins': HandCoins,
  landmark: Landmark,
  wallet: Wallet,
};

/** EntryIcon 渲染条目声明的图标；名字未登记时不渲染。 */
export function EntryIcon({ name, size = 20 }: { name: string; size?: number }) {
  const Icon = icons[name];
  if (!Icon) {
    return null;
  }
  return <Icon size={size} aria-hidden />;
}

/** ConsoleLayout 是受保护布局，挂在控制台各页面之外。 */
export function ConsoleLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  // 回跳地址取挂载时的站内地址：页面之间切换不该重取会话与清单，所以它不进依赖。
  const from = useRef(location.pathname + location.search);
  const [user, setUser] = useState<SessionUser | null>(null);
  const [manifest, setManifest] = useState<ConsoleData | null>(null);
  const [denied, setDenied] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    // 身份与清单一起取：两者都由服务端下发，只到一半不构成可用状态。
    Promise.all([session(), loadConsole()])
      .then(([who, data]) => {
        if (!active) return;
        setUser(who);
        setManifest(data);
      })
      .catch((err: unknown) => {
        if (!active || !(err instanceof ApiError)) return;
        if (err.code === Code.Unauthorized) {
          // 未登录或会话失效：带上站内来源地址回登录页，登录后回到原页面。
          navigate(`/login?redirect=${encodeURIComponent(from.current)}`, { replace: true });
          return;
        }
        if (err.code === Code.Forbidden) {
          // 已登录但服务端拒绝：会话有效却没有可用的控制台，展示 403 页面。
          setDenied(err.message);
        }
      });
    return () => {
      active = false;
    };
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

  if (denied) {
    return (
      <main className="mx-auto max-w-lg px-4 py-10">
        <Forbidden message={denied} />
        <div className="mt-4 text-center">
          <button
            type="button"
            onClick={() => void logout()}
            className="min-h-10 rounded-lg border border-edge px-4 text-sm text-danger hover:border-danger"
          >
            退出登录
          </button>
        </div>
      </main>
    );
  }

  if (!user || !manifest) {
    return (
      <main className="flex min-h-screen items-center justify-center text-sm text-muted">
        正在加载…
      </main>
    );
  }

  // 条目与能力都来自服务端：没有该路径的条目、或条目所需能力不在集合里，都按无权处理。
  const entry = entryForPath(manifest, location.pathname);
  const reachable = entry !== null && allows(manifest, entry.capability);

  return (
    <div className="min-h-screen pb-24">
      <header className="border-b border-edge bg-surface">
        <div className="mx-auto flex max-w-3xl items-center gap-3 px-4 py-3">
          <Logo size={28} />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">{user.username}</div>
            <div className="truncate text-xs text-muted">
              身份 {user.roles.join('、')} · 登录方式 {user.identities.join('、')}
            </div>
          </div>
          <button
            type="button"
            onClick={() => void logout()}
            className="min-h-10 shrink-0 rounded-lg border border-edge px-4 text-sm text-danger hover:border-danger"
          >
            退出登录
          </button>
        </div>
      </header>

      <main className="mx-auto max-w-3xl px-4 py-6">
        {reachable ? <Outlet context={manifest} /> : <Forbidden />}
      </main>

      <nav aria-label="控制台导航" className="fixed inset-x-0 bottom-0 border-t border-edge bg-surface">
        <ul className="mx-auto flex max-w-3xl">
          {manifest.navigation.map((item) => (
            <li key={item.path} className="flex-1">
              <NavLink
                to={item.path}
                end={item.path === '/'}
                className={({ isActive }) =>
                  `flex min-h-14 flex-col items-center justify-center gap-1 text-xs ${
                    isActive ? 'text-action' : 'text-muted'
                  }`
                }
              >
                <EntryIcon name={item.icon} />
                {item.title}
              </NavLink>
            </li>
          ))}
        </ul>
      </nav>
    </div>
  );
}
