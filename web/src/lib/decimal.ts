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

/**
 * addDecimals 把若干十进制字符串相加，返回规范化取值；有非十进制形态的输入时返回 null。
 *
 * 合计不借浮点：金额列是 DECIMAL(20,8)，整数部分可达 12 位，`Number` 会把末位改掉。
 * 做法是拆出符号、整数位与小数位，把各项对齐到最大小数位后用 BigInt 累加，再还原成
 * 十进制串。输出形态与 decimal.String() 一致：不带多余尾零（`1234.5`、`0`、`2000000`）。
 *
 * 返回 null 而不是猜一个数：调用方据此显示占位符，避免把异常数据加成看似合理的金额。
 */
export function addDecimals(values: string[]): string | null {
  const parts: ParsedDecimal[] = [];
  let scale = 0;
  for (const value of values) {
    const part = parseDecimal(value);
    if (part === null) {
      return null;
    }
    parts.push(part);
    scale = Math.max(scale, part.fraction.length);
  }
  let total = 0n;
  for (const part of parts) {
    const aligned = `${part.whole}${part.fraction.padEnd(scale, '0')}`;
    total += part.negative ? -BigInt(aligned) : BigInt(aligned);
  }
  return formatScaledDigits(total, scale);
}

/** ParsedDecimal 是一个拆开的十进制串：符号、整数位与小数位。 */
interface ParsedDecimal {
  negative: boolean;
  whole: string;
  fraction: string;
}

/** parseDecimal 拆解一个十进制串；带符号、非数字与多小数点的形态都返回 null。 */
function parseDecimal(raw: string): ParsedDecimal | null {
  const matched = /^([+-]?)([0-9]+)(?:\.([0-9]+))?$/.exec(raw.trim());
  if (matched === null) {
    return null;
  }
  return { negative: matched[1] === '-', whole: matched[2], fraction: matched[3] ?? '' };
}

/** formatScaledDigits 把「已乘 10^scale 的整数」还原成十进制串，去掉多余尾零。 */
function formatScaledDigits(total: bigint, scale: number): string {
  const negative = total < 0n;
  const digits = (negative ? -total : total).toString().padStart(scale + 1, '0');
  const whole = digits.slice(0, digits.length - scale).replace(/^0+(?=[0-9])/, '');
  const fraction = digits.slice(digits.length - scale).replace(/0+$/, '');
  const text = fraction === '' ? whole : `${whole}.${fraction}`;
  return negative && text !== '0' ? `-${text}` : text;
}

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
