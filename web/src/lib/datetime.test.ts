import { describe, expect, it } from 'vitest';

import { formatTimestamp } from './datetime';

describe('formatTimestamp', () => {
  it('无取值时给占位符', () => {
    expect(formatTimestamp(null)).toBe('—');
    expect(formatTimestamp('')).toBe('—');
  });

  it('RFC3339 时间按本地时区显示到分钟', () => {
    // 时区不同会落在 10/07 或 10/08，断言到「年月日 + 时分」的形状而不是固定时刻。
    expect(formatTimestamp('2026-10-08T08:01:44Z')).toMatch(/^2026\/10\/0[78] [0-9]{2}:[0-9]{2}$/);
  });

  it('解析不了的值原样返回', () => {
    expect(formatTimestamp('not-a-time')).toBe('not-a-time');
  });
});
