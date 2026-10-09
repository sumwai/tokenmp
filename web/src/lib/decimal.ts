/**
 * decimal 字符串在展示层的格式化。
 *
 * 契约里的金额与用量都是十进制字符串：服务端把 decimal 原样序列化，取值可能超出
 * IEEE754 双精度能精确表示的范围（20 位整数、多小数位的折算结果）。因此补零与千分位
 * 全程按字符串处理，不经 `Number` / `parseFloat`（web/AGENTS.md 的「数据展示」）。
 *
 * 精度只补不截：小数位多于展示位数时原样保留。舍入属于计费口径，展示层不替计费决定。
 */

/** UNIT_SCALE 是结算单位对应的展示小数位；未列出的单位不补小数位。 */
const UNIT_SCALE: Record<string, number> = {
  currency: 2,
  credit: 2,
  token: 0,
};

/** formatUnitAmount 按结算单位格式化一个十进制字符串；未知单位按不补小数位处理。 */
export function formatUnitAmount(unit: string, raw: string): string {
  return formatDecimal(raw, UNIT_SCALE[unit] ?? 0);
}

/**
 * formatDecimal 给整数部分加千分位，并把小数位补到 scale 位。
 *
 * 形态不是十进制的取值（空串、`n/a` 一类异常数据）原样返回：展示层不替上游猜数值，
 * 露出原值比给出一个看起来合理的错数更容易发现。
 */
export function formatDecimal(raw: string, scale: number): string {
  const text = raw.trim();
  const negative = text.startsWith('-');
  const digits = negative ? text.slice(1) : text;
  const dot = digits.indexOf('.');
  const whole = dot < 0 ? digits : digits.slice(0, dot);
  const fraction = dot < 0 ? '' : digits.slice(dot + 1);
  if (!/^[0-9]+$/.test(whole) || !/^[0-9]*$/.test(fraction)) {
    return raw;
  }
  const grouped = whole.replace(/\B(?=([0-9]{3})+$)/g, ',');
  const padded = fraction.padEnd(scale > 0 ? scale : 0, '0');
  return `${negative ? '-' : ''}${grouped}${padded === '' ? '' : `.${padded}`}`;
}

/**
 * parseUnsignedInt 解析一个非负整数串，接受不了的形态返回 null。
 *
 * 用途是把 URL 参数与响应头里的计数解析成整数（流水条数、Retry-After 秒数），按位累加
 * 而不经 `Number`：与金额、用量同一条约定，需要精度的数值不借浮点。只接受纯数字形态，
 * 带符号、小数与指数都不是计数。
 */
export function parseUnsignedInt(raw: string): number | null {
  const text = raw.trim();
  if (text === '' || !/^[0-9]+$/.test(text)) {
    return null;
  }
  const zero = '0'.charCodeAt(0);
  let value = 0;
  for (const digit of text) {
    value = value * 10 + (digit.charCodeAt(0) - zero);
  }
  return value;
}
