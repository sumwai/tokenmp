import { useEffect, useRef, useState } from 'react';

/**
 * 长列表的窗口化：只渲染视口内（含过扫描）的行。
 *
 * 计算与状态分离：区间由纯函数 virtualWindow 决定，便于单测；hook 只负责把
 * 滚动位置与容器高度接进来。行高固定，因此不需要测量每一行 —— 列表行按固定高度
 * 排版（web/AGENTS.md 的移动端长列表要求），高度变化会让窗口计算失真。
 */

/** VirtualWindow 是当前要渲染的行区间与占位尺寸。 */
export interface VirtualWindow {
  /** 首个要渲染的行下标（含）。 */
  start: number;
  /** 末个要渲染的行下标（不含）。 */
  end: number;
  /** 渲染区之前要留出的高度，维持滚动条长度。 */
  offsetY: number;
  /** 全部行的总高度。 */
  totalHeight: number;
}

/** VirtualWindowInput 是窗口计算的输入。 */
export interface VirtualWindowInput {
  /** 容器已滚动的距离。 */
  scrollTop: number;
  /** 容器可视高度。 */
  viewportHeight: number;
  /** 单行高度。 */
  rowHeight: number;
  /** 总行数。 */
  count: number;
  /** 视口外额外渲染的行数，默认 6；用于滚动时少出现空白。 */
  overscan?: number;
}

/**
 * virtualWindow 计算要渲染的行区间。
 *
 * 行高或行数非正时返回空区间：调用方据此退化到「不渲染行」，而不是渲染 NaN 个节点。
 */
export function virtualWindow(input: VirtualWindowInput): VirtualWindow {
  const { scrollTop, viewportHeight, rowHeight, count, overscan = 6 } = input;
  if (count <= 0 || rowHeight <= 0) {
    return { start: 0, end: 0, offsetY: 0, totalHeight: 0 };
  }
  const totalHeight = count * rowHeight;
  const first = Math.floor(Math.max(scrollTop, 0) / rowHeight) - overscan;
  const visible = Math.ceil(Math.max(viewportHeight, 0) / rowHeight) + overscan * 2;
  const start = Math.min(Math.max(first, 0), count);
  const end = Math.min(start + Math.max(visible, 1), count);
  return { start, end, offsetY: start * rowHeight, totalHeight };
}

/** VirtualRows 是 hook 的返回值。 */
export interface VirtualRows {
  /** 挂在滚动容器上的 ref。 */
  ref: React.RefObject<HTMLDivElement | null>;
  /** 当前要渲染的区间。 */
  window: VirtualWindow;
}

/** useVirtualRows 把滚动容器的位置与高度接进窗口计算。 */
export function useVirtualRows(count: number, rowHeight: number): VirtualRows {
  const ref = useRef<HTMLDivElement | null>(null);
  const [state, setState] = useState({ scrollTop: 0, viewportHeight: 0 });

  useEffect(() => {
    const node = ref.current;
    if (!node) {
      return;
    }
    const sync = () => {
      setState({ scrollTop: node.scrollTop, viewportHeight: node.clientHeight });
    };
    sync();
    node.addEventListener('scroll', sync, { passive: true });
    window.addEventListener('resize', sync);
    return () => {
      node.removeEventListener('scroll', sync);
      window.removeEventListener('resize', sync);
    };
  }, [count, rowHeight]);

  return {
    ref,
    window: virtualWindow({ ...state, rowHeight, count }),
  };
}
