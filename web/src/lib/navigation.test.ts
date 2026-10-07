import { describe, expect, it } from 'vitest';

import { safeRedirect } from './navigation';

describe('safeRedirect', () => {
  it('放行站内相对路径', () => {
    expect(safeRedirect('/panel/keys')).toBe('/panel/keys');
    expect(safeRedirect('/')).toBe('/');
  });

  it('拒绝外站与协议相对路径', () => {
    expect(safeRedirect('https://evil.example.com')).toBe('/');
    expect(safeRedirect('//evil.example.com')).toBe('/');
    expect(safeRedirect('/\\evil.example.com')).toBe('/');
  });

  it('拒绝回跳到登录页自身，避免循环', () => {
    expect(safeRedirect('/login?redirect=/x')).toBe('/');
  });

  it('空值回落首页', () => {
    expect(safeRedirect(null)).toBe('/');
    expect(safeRedirect('')).toBe('/');
  });
});
