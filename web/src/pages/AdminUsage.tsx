import { AdminDecimal, AdminListPage, AdminNumber, AdminText, AdminTime, type AdminColumn } from '../components/admin';
import { FilterAccountID, FilterSince, listAdminUsage, type AdminUsageItem } from '../lib/admin';

/** 全平台用量流水页：跨账户列出每次调用的用量与结算取值。 */

const columns: AdminColumn<AdminUsageItem>[] = [
  { id: 'account_id', header: '账户', cell: (row) => <AdminNumber value={row.account_id} /> },
  { id: 'model', header: '模型', cell: (row) => <span className="font-medium">{row.model}</span> },
  { id: 'usage', header: '用量', cell: (row) => <UsageCell usage={row.usage} /> },
  { id: 'gross_amount', header: '应扣量', cell: (row) => <AdminDecimal value={row.gross_amount} /> },
  { id: 'multiplier', header: '倍率', hideBelow: 'md', cell: (row) => <AdminDecimal value={row.multiplier} /> },
  { id: 'merchant_id', header: '商家', hideBelow: 'lg', cell: (row) => <AdminNumber value={row.merchant_id} /> },
  { id: 'channel_id', header: '渠道', hideBelow: 'lg', cell: (row) => <AdminNumber value={row.channel_id} /> },
  { id: 'created_at', header: '时刻', cell: (row) => <AdminTime value={row.created_at} empty="—" /> },
];

/** UsageCell 逐条列出用量分量：值是十进制字符串，原样展示。 */
function UsageCell({ usage }: { usage: Record<string, string> }) {
  const entries = Object.entries(usage);
  if (entries.length === 0) {
    return <AdminText value="" />;
  }
  return (
    <ul className="space-y-0.5 text-xs tabular-nums">
      {entries.map(([metric, amount]) => (
        <li key={metric}>
          <span className="text-muted">{metric}</span> {amount}
        </li>
      ))}
    </ul>
  );
}

/** AdminUsage 渲染全平台用量流水。 */
export function AdminUsage() {
  return (
    <AdminListPage<AdminUsageItem>
      title="流水"
      description="全平台用量流水；每次成功调用一行，按写入顺序排列。"
      columns={columns}
      load={listAdminUsage}
      filters={[
        { key: FilterAccountID, label: '账户 id', placeholder: '正整数，留空列出全部' },
        { key: FilterSince, label: '起始时刻', placeholder: 'RFC3339，如 2026-10-09T00:00:00Z' },
      ]}
      emptyText="该区间内没有用量流水。放宽条件或清除筛选后再看。"
    />
  );
}
