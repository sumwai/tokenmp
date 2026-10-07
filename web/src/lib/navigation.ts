/** 登录回跳的站内路径校验：只放行站内相对路径，排除登录页自身。 */
export function safeRedirect(raw: string | null): string {
  if (!raw) return '/';
  // 浏览器会把路径开头的反斜杠规范化为斜杠："/\host" 与 "//host" 同为
  // 协议相对跳转，两者都必须拒绝，否则回跳参数可被用于外站重定向。
  if (
    !raw.startsWith('/') ||
    raw.startsWith('//') ||
    raw.startsWith('/\\') ||
    raw.startsWith('/login')
  ) {
    return '/';
  }
  return raw;
}
