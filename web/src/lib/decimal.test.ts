import { describe, expect, it } from 'vitest';

import { addDecimals, formatDecimal, formatUnitAmount, parseUnsignedInt } from './decimal';

describe('formatDecimal', () => {
  it('整数部分加千分位，小数位补到展示位数', () => {
    expect(formatDecimal('1234567.5', 2)).toBe('1,234,567.50');
    expect(formatDecimal('1000', 2)).toBe('1,000.00');
    expect(formatDecimal('0', 2)).toBe('0.00');
    expect(formatDecimal('999', 0)).toBe('999');
    expect(formatDecimal('1000', 0)).toBe('1,000');
  });

  it('负数保留符号，符号不参与分组', () => {
    expect(formatDecimal('-1234.5', 2)).toBe('-1,234.50');
    expect(formatDecimal('-100', 0)).toBe('-100');
  });

  it('超出双精度的大数逐位保真：经 Number 会丢末位', () => {
    // 2^53 + 1：Number 只能表示到 ...992，字符串路径必须给出 ...993。
    expect(formatDecimal('9007199254740993', 2)).toBe('9,007,199,254,740,993.00');
    // 30 位整数：Number 会退化成指数形式 "1.2345678901234568e+29"。
    expect(formatDecimal('123456789012345678901234567890', 0)).toBe(
      '123,456,789,012,345,678,901,234,567,890',
    );
  });

  it('小数位多于展示位数时原样保留，不做舍入', () => {
    expect(formatDecimal('0.123456789', 2)).toBe('0.123456789');
    expect(formatDecimal('1.0000005', 2)).toBe('1.0000005');
  });

  it('非十进制形态原样返回', () => {
    expect(formatDecimal('', 2)).toBe('');
    expect(formatDecimal('n/a', 2)).toBe('n/a');
    expect(formatDecimal('1,000', 2)).toBe('1,000');
    expect(formatDecimal('12abc', 2)).toBe('12abc');
  });
});

describe('addDecimals', () => {
  it('整数与小数按位相加，结果不带多余尾零', () => {
    expect(addDecimals(['1', '2'])).toBe('3');
    expect(addDecimals(['0.1', '0.2'])).toBe('0.3');
    expect(addDecimals(['1234.5', '1'])).toBe('1235.5');
    expect(addDecimals(['1.999', '0.001'])).toBe('2');
    expect(addDecimals([])).toBe('0');
  });

  it('超出双精度的大数逐位保真：经 Number 会丢末位', () => {
    expect(addDecimals(['9007199254740993', '1'])).toBe('9007199254740994');
    expect(addDecimals(['123456789012345678901234567890', '1'])).toBe(
      '123456789012345678901234567891',
    );
  });

  it('负数与零按符号相加', () => {
    expect(addDecimals(['1.5', '-0.5'])).toBe('1');
    expect(addDecimals(['1', '-3'])).toBe('-2');
    expect(addDecimals(['1', '-1'])).toBe('0');
  });

  it('非十进制形态的取值使合计取不回，返回 null', () => {
    expect(addDecimals(['1', ''])).toBeNull();
    expect(addDecimals(['n/a'])).toBeNull();
    expect(addDecimals(['1,000'])).toBeNull();
    expect(addDecimals(['1.2.3'])).toBeNull();
  });
});

describe('formatUnitAmount', () => {
  it('按结算单位取展示小数位', () => {
    expect(formatUnitAmount('currency', '1000')).toBe('1,000.00');
    expect(formatUnitAmount('credit', '12.5')).toBe('12.50');
    expect(formatUnitAmount('token', '1500')).toBe('1,500');
    expect(formatUnitAmount('token', '1500.25')).toBe('1,500.25');
  });

  it('未知单位只加千分位，不臆造小数位', () => {
    expect(formatUnitAmount('usd', '1000')).toBe('1,000');
  });
});

describe('parseUnsignedInt', () => {
  it('解析纯数字串', () => {
    expect(parseUnsignedInt('0')).toBe(0);
    expect(parseUnsignedInt('100')).toBe(100);
    expect(parseUnsignedInt(' 25 ')).toBe(25);
  });

  it('非纯数字形态返回 null，不做部分解析', () => {
    expect(parseUnsignedInt('')).toBeNull();
    expect(parseUnsignedInt('-1')).toBeNull();
    expect(parseUnsignedInt('10.5')).toBeNull();
    expect(parseUnsignedInt('12abc')).toBeNull();
    expect(parseUnsignedInt('abc')).toBeNull();
    expect(parseUnsignedInt('1e3')).toBeNull();
  });
});
