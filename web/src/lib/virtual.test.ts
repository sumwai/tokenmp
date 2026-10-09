import { describe, expect, it } from 'vitest';

import { virtualWindow } from './virtual';

/** 窗口计算：行区间、占位高度与边界退化。 */

describe('virtualWindow', () => {
  it('视口装得下全部行时渲染全部', () => {
    const window = virtualWindow({
      scrollTop: 0,
      viewportHeight: 400,
      rowHeight: 72,
      count: 5,
      overscan: 2,
    });
    expect(window.start).toBe(0);
    expect(window.end).toBe(5);
    expect(window.totalHeight).toBe(360);
  });

  it('滚动后只渲染视口附近的行，并留出占位高度', () => {
    const window = virtualWindow({
      scrollTop: 720,
      viewportHeight: 288,
      rowHeight: 72,
      count: 100,
      overscan: 1,
    });
    expect(window.start).toBe(9);
    expect(window.end).toBe(15);
    expect(window.offsetY).toBe(9 * 72);
    expect(window.totalHeight).toBe(7200);
  });

  it('滚到底部时区间不越界', () => {
    const window = virtualWindow({
      scrollTop: 7200,
      viewportHeight: 288,
      rowHeight: 72,
      count: 100,
      overscan: 3,
    });
    expect(window.end).toBe(100);
    expect(window.start).toBeLessThan(100);
  });

  it('负数滚动位置按 0 处理', () => {
    const window = virtualWindow({
      scrollTop: -50,
      viewportHeight: 144,
      rowHeight: 72,
      count: 10,
    });
    expect(window.start).toBe(0);
  });

  it('空列表与非正行高退化到空区间', () => {
    expect(virtualWindow({ scrollTop: 0, viewportHeight: 300, rowHeight: 72, count: 0 })).toEqual({
      start: 0,
      end: 0,
      offsetY: 0,
      totalHeight: 0,
    });
    expect(virtualWindow({ scrollTop: 0, viewportHeight: 300, rowHeight: 0, count: 10 })).toEqual({
      start: 0,
      end: 0,
      offsetY: 0,
      totalHeight: 0,
    });
  });
});
