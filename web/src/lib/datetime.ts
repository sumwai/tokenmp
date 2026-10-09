/**
 * 时间的展示格式化。
 *
 * 契约里的时间字段是 RFC3339，页面按浏览器本地时区显示；解析不了的值原样给出，
 * 避免把一个异常时间显示成某个看起来正常的时刻。
 */

/** formatTimestamp 把 RFC3339 时间格式化成本地时间；无取值（`null`）时给占位符。 */
export function formatTimestamp(raw: string | null): string {
  if (!raw) {
    return '—';
  }
  const at = new Date(raw);
  if (at.toString() === 'Invalid Date') {
    return raw;
  }
  return at.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}
