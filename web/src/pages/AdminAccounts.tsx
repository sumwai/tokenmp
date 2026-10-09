import {
  AdminDecimal,
  AdminListPage,
  AdminNumber,
  AdminText,
  type AdminColumn,
} from '../components/admin';
import { listAdminAccounts, type AdminAccount } from '../lib/admin';

/** 账户清单页：归属、倍率与状态。 */

const columns: AdminColumn<AdminAccount>[] = [
  { id: 'code', header: '账户编码', cell: (row) => <span className="font-medium">{row.code}</span> },
  { id: 'name', header: '名称', hideBelow: 'md', cell: (row) => <AdminText value={row.name} /> },
  { id: 'owner', header: '归属主体', hideBelow: 'lg', cell: (row) => <AdminNumber value={row.owner_user_id ?? null} /> },
  {
    id: 'merchant',
    header: '默认商家',
    hideBelow: 'lg',
    cell: (row) => <AdminNumber value={row.default_merchant_id ?? null} />,
  },
  {
    id: 'price_multiplier',
    header: '价格倍率',
    cell: (row) => <AdminDecimal value={row.price_multiplier} />,
  },
  { id: 'status', header: '状态', cell: (row) => <AdminText value={statusText(row.status)} /> },
];

/** statusText 把账户状态写成产品文案。 */
function statusText(status: string): string {
  if (status === 'active') return '在用';
  if (status === 'disabled') return '已停用';
  return status;
}

/** AdminAccounts 渲染账户清单。 */
export function AdminAccounts() {
  return (
    <AdminListPage<AdminAccount>
      title="账户"
      description="平台账户的编码、归属、价格倍率与状态。"
      columns={columns}
      load={listAdminAccounts}
      emptyText="还没有账户。开户后刷新即见。"
    />
  );
}
