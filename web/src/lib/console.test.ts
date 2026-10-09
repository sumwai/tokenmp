import { describe, expect, it } from 'vitest';

import { allows, entryForPath } from './console';
import type { ConsoleData } from './console';

/** 清单夹具：持有首页与密钥能力，段落里另有一个请求记录入口。 */
const manifest: ConsoleData = {
  roles: ['member'],
  capabilities: ['console', 'keys'],
  navigation: [
    { title: '首页', description: '', icon: 'house', path: '/', capability: 'console' },
    {
      title: '密钥',
      description: '签发与吊销调用密钥',
      icon: 'key-round',
      path: '/keys',
      capability: 'keys',
    },
  ],
  sections: [
    {
      title: '排障',
      description: '按请求标识定位问题',
      entries: [
        {
          title: '请求记录',
          description: '脱敏报文与尝试时间线',
          icon: 'scroll-text',
          path: '/requests',
          capability: 'requests',
        },
      ],
    },
  ],
};

describe('entryForPath', () => {
  it('导航项与段落条目都按路径定位', () => {
    expect(entryForPath(manifest, '/')?.title).toBe('首页');
    expect(entryForPath(manifest, '/requests')?.title).toBe('请求记录');
  });

  it('清单里没有的路径返回 null：骨架据此渲染无权访问页', () => {
    expect(entryForPath(manifest, '/usage')).toBeNull();
  });

  it('二级页归属到它的入口：详情深链不被判成无权访问', () => {
    expect(entryForPath(manifest, '/requests/req_abc')?.title).toBe('请求记录');
  });

  it('根路径只按精确匹配，不覆盖其余路径', () => {
    expect(entryForPath(manifest, '/unknown')?.title).toBeUndefined();
    expect(entryForPath(manifest, '/unknown')).toBeNull();
  });
});

describe('allows', () => {
  it('按能力集合判定，不按身份取值', () => {
    expect(allows(manifest, 'keys')).toBe(true);
    expect(allows(manifest, 'ops')).toBe(false);
  });

  it('渲染只看能力：身份不同而能力相同，判定一致', () => {
    // 身份叠加后 admin 也能是普通调用方：能不能进页面取决于服务端下发的能力集合，
    // 与身份集合无关（能力里没有 ops 就进不去管理面）。
    const admin = { ...manifest, roles: ['member', 'partner', 'admin'] };
    expect(allows(admin, 'keys')).toBe(true);
    expect(allows(admin, 'ops')).toBe(false);
    expect(entryForPath(admin, '/keys')?.title).toBe('密钥');
  });
});

describe('前端零角色分支', () => {
  // 组件与页面不得出现角色取值字面量：导航与能力都来自服务端清单（web/AGENTS.md「多角色」）。
  const sources = import.meta.glob('../**/*.{ts,tsx}', {
    query: '?raw',
    import: 'default',
    eager: true,
  }) as Record<string, string>;
  const roleLiteral = /['"`](member|partner|admin)['"`]/;

  /**
   * 路由段与身份取值同名（商家域的 `/partner`）：`path="partner"` 是路径，不是身份分支，
   * 骨架按服务端清单下发的 path 匹配入口。扫描前只抹掉路由声明里的 path 字面量，
   * 其余位置照旧一律拦下 —— 把 `role === 'partner'` 这类分支放行才是真的放水。
   */
  const routePath = /\bpath=\{?["'`][^"'`]*["'`]\}?/g;

  it('web/src 里没有角色取值字面量', () => {
    const offenders = Object.entries(sources)
      .filter(([path]) => !path.includes('.test.'))
      // 生成物镜像契约，必然出现身份取值（SessionUser.roles 的 enum）；红线针对手写代码。
      .filter(([path]) => !path.includes('/generated/'))
      .filter(([, text]) => roleLiteral.test(text.replace(routePath, 'path=')))
      .map(([path]) => path);
    expect(offenders).toEqual([]);
  });

  it('扫描范围非空，避免断言在空集合上通过', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(5);
  });
});
