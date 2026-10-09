import { Link, useOutletContext } from 'react-router-dom';

import { EntryIcon } from '../components/console';
import type { ConsoleData } from '../lib/console';

/**
 * 首页：服务端下发的段落与入口。
 *
 * 身份、登出与导航在骨架里，本页只渲染清单内容；组件不认识角色。
 */
export function Home() {
  const manifest = useOutletContext<ConsoleData>();

  return (
    <div>
      <h1 className="text-lg font-bold">控制台</h1>

      {manifest.sections.length === 0 ? (
        <p className="mt-4 rounded-2xl border border-edge bg-surface p-5 text-sm text-muted">
          当前身份没有可用的控制台入口，请联系平台管理员。
        </p>
      ) : null}

      {manifest.sections.map((section) => (
        <section key={section.title} className="mt-5">
          <h2 className="text-sm font-semibold">{section.title}</h2>
          {section.description ? (
            <p className="mt-1 text-xs text-muted">{section.description}</p>
          ) : null}
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            {section.entries.map((entry) => (
              <Link
                key={entry.path}
                to={entry.path}
                className="flex items-start gap-3 rounded-2xl border border-edge bg-surface p-4 hover:border-action"
              >
                <span className="mt-0.5 text-action">
                  <EntryIcon name={entry.icon} />
                </span>
                <span className="min-w-0">
                  <span className="block text-sm font-semibold">{entry.title}</span>
                  {entry.description ? (
                    <span className="mt-1 block text-xs text-muted">{entry.description}</span>
                  ) : null}
                </span>
              </Link>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}
