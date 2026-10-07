import type { ReactNode } from 'react';

/** Logo mark：品牌资产，保持固定配色，不随主题与文字色变化（web/AGENTS.md）。 */
export function Logo({ size = 48 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" aria-label="TokenMP">
      <rect width="64" height="64" rx="16" fill="#111827" />
      <rect x="12" y="34" width="18" height="18" rx="5" fill="#fff" opacity=".92" />
      <rect x="34" y="12" width="18" height="18" rx="5" fill="#fff" opacity=".5" />
      <circle cx="32" cy="32" r="10" fill="#fff" />
    </svg>
  );
}

/** AuthShell 是认证页的统一骨架：居中卡片、品牌区、标题与页脚。 */
export function AuthShell({
  title,
  subtitle,
  children,
  footer,
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <main className="flex min-h-screen items-center justify-center px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex flex-col items-center gap-3">
          <Logo />
          <div className="text-center">
            <h1 className="text-xl font-bold">{title}</h1>
            {subtitle ? <p className="mt-1 text-sm text-muted">{subtitle}</p> : null}
          </div>
        </div>
        <div className="rounded-2xl border border-edge bg-surface p-6 shadow-sm">{children}</div>
        {footer ? <div className="mt-4 text-center text-sm text-muted">{footer}</div> : null}
      </div>
    </main>
  );
}

/** ErrorBanner 展示服务端返回的用户文案；按业务码取边框色。 */
export function ErrorBanner({ code, message }: { code: number; message: string }) {
  if (!message) return null;
  const tone =
    code === 429
      ? 'border-warn/40 bg-warn/10 text-warn'
      : code >= 500
        ? 'border-danger/40 bg-danger/10 text-danger'
        : 'border-danger/30 bg-danger/5 text-danger';
  return (
    <div role="alert" className={`mb-4 rounded-lg border px-3 py-2 text-sm ${tone}`}>
      {message}
    </div>
  );
}

/** Field 是统一的表单字段：标签在上、输入框整宽。 */
export function Field({
  label,
  type = 'text',
  value,
  onChange,
  autoComplete,
  required = true,
}: {
  label: string;
  type?: string;
  value: string;
  onChange: (value: string) => void;
  autoComplete?: string;
  required?: boolean;
}) {
  return (
    <label className="mb-4 block">
      <span className="mb-1.5 block text-xs text-muted">{label}</span>
      <input
        type={type}
        value={value}
        required={required}
        autoComplete={autoComplete}
        onChange={(e) => onChange(e.currentTarget.value)}
        className="min-h-11 w-full rounded-lg border border-edge bg-surface px-3 text-base outline-none focus:border-action"
      />
    </label>
  );
}

/** SubmitButton 是统一的主操作按钮，带忙态。 */
export function SubmitButton({ busy, children }: { busy: boolean; children: ReactNode }) {
  return (
    <button
      type="submit"
      disabled={busy}
      className="min-h-11 w-full rounded-lg bg-action font-semibold text-white disabled:opacity-60"
    >
      {busy ? '请稍候…' : children}
    </button>
  );
}
