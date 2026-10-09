import { ShieldAlert } from 'lucide-react';
import { Link } from 'react-router-dom';

/**
 * 403 页面：已登录但当前身份无权访问。
 *
 * 文案取服务端下发的 message（如「账户尚未开通」），没有时用兜底文案。
 */
export function Forbidden({ message }: { message?: string }) {
  return (
    <div className="rounded-2xl border border-edge bg-surface p-6 text-center">
      <ShieldAlert size={24} className="mx-auto text-warn" aria-hidden />
      <h1 className="mt-3 text-base font-semibold">无权访问</h1>
      <p className="mt-1 text-sm text-muted">{message || '当前身份无权访问该页面。'}</p>
      <Link
        to="/"
        className="mt-4 inline-flex min-h-10 items-center rounded-lg border border-edge px-4 text-sm hover:border-action"
      >
        返回首页
      </Link>
    </div>
  );
}
