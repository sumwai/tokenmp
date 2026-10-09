import { describe, expect, it } from 'vitest';

import { loginRedirect, safeRedirect } from './navigation';

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

describe('loginRedirect', () => {
  it('把当前地址编码进 redirect，筛选与分页一并带回', () => {
    expect(loginRedirect('/keys?page=2&enabled=false')).toBe(
      '/login?redirect=%2Fkeys%3Fpage%3D2%26enabled%3Dfalse',
    );
  });

  it('编码后的地址能还原成站内路径', () => {
    const target = '/keys?page=2&enabled=false';
    const raw = new URL(loginRedirect(target), 'https://console.example').searchParams.get(
      'redirect',
    );
    expect(safeRedirect(raw)).toBe(target);
  });
});
