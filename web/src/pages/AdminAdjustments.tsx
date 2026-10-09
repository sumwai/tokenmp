import { AdminDecimal, AdminListPage, AdminNumber, AdminText, AdminTime, type AdminColumn } from '../components/admin';
import { FilterAccountID, listAdminAdjustments, type AdminAdjustment } from '../lib/admin';

/** 调账清单页：人工补扣与退费记录。 */

const columns: AdminColumn<AdminAdjustment>[] = [
  { id: 'account_id', header: '账户', cell: (row) => <AdminNumber value={row.account_id} /> },
  { id: 'delta_amount', header: '调整量', cell: (row) => <AdminDecimal value={row.delta_amount} /> },
  { id: 'reason', header: '原因', hideBelow: 'md', cell: (row) => <AdminText value={row.reason} /> },
  { id: 'operator', header: '操作者', hideBelow: 'lg', cell: (row) => <AdminText value={row.operator} /> },
  { id: 'created_at', header: '时刻', cell: (row) => <AdminTime value={row.created_at} empty="—" /> },
];

/** AdminAdjustments 渲染调账清单。 */
export function AdminAdjustments() {
  return (
    <AdminListPage<AdminAdjustment>
      title="调账"
      description="人工补扣与退费记录；调整量的正负表示补扣与退费。"
      columns={columns}
      load={listAdminAdjustments}
      filters={[{ key: FilterAccountID, label: '账户 id', placeholder: '正整数，留空列出全部' }]}
      emptyText="还没有调账记录。写入一条调账后刷新即见。"
    />
  );
}
