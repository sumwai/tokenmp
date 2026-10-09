import { api } from './generated/api';
import type { components } from './generated/schema';

/**
 * 控制台清单：导航项、首页段落与能力集合，与 docs/openapi-web.yaml 的 ConsoleData 对齐。
 *
 * 角色与权限的真相在服务端：本文件只做取数与按路径定位，不出现角色取值。
 */

/** ConsoleEntry 是一条控制台条目：导航项与首页段落条目同一形状。 */
export type ConsoleEntry = components['schemas']['ConsoleEntry'];

/** ConsoleSection 是首页的一个段落。 */
export type ConsoleSection = components['schemas']['ConsoleSection'];

/** ConsoleData 是控制台清单。 */
export type ConsoleData = components['schemas']['ConsoleData'];

/** loadConsole 取当前主体的控制台清单。 */
export async function loadConsole(): Promise<ConsoleData> {
  return api.getUserConsole();
}

/**
 * entryForPath 在清单里按路径定位条目；导航项与首页段落条目都在查找范围内。
 *
 * 二级页按前缀归属到它的入口：`/requests/abc` 属于清单里的 `/requests`，
 * 否则详情深链会被骨架判成无权访问。根路径 `/` 只按精确匹配，避免它覆盖所有路径。
 *
 * 找不到即当前主体没有该路径的条目，骨架据此渲染无权访问页。
 */
export function entryForPath(data: ConsoleData, pathname: string): ConsoleEntry | null {
  const inNavigation = data.navigation.find((entry) => coversPath(entry.path, pathname));
  if (inNavigation) {
    return inNavigation;
  }
  for (const section of data.sections) {
    const inSection = section.entries.find((entry) => coversPath(entry.path, pathname));
    if (inSection) {
      return inSection;
    }
  }
  return null;
}

/** coversPath 判断入口路径是否覆盖当前路径：自身，或它之下的一级/多级后代路径。 */
function coversPath(entryPath: string, pathname: string): boolean {
  if (entryPath === pathname) {
    return true;
  }
  return entryPath !== '/' && pathname.startsWith(`${entryPath}/`);
}

/**
 * allows 判断能力集合里是否含目标能力。
 *
 * 条目也自带所需能力：两者不一致时（服务端版本更旧或更新）以能力集合为准 ——
 * 权限的真相是能力，条目只是入口。
 */
export function allows(data: ConsoleData, capability: string): boolean {
  return data.capabilities.includes(capability);
}
