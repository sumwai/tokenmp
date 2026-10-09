import { AdminFlag, AdminListPage, AdminNumber, AdminTime, type AdminColumn } from '../components/admin';
import { FilterMerchantID, FilterModel, listAdminPricing, type AdminPricing } from '../lib/admin';

/** 定价版本清单页：按商家与模型过滤，未退役的版本即当前生效版本。 */

const columns: AdminColumn<AdminPricing>[] = [
  { id: 'model', header: '模型', cell: (row) => <span className="font-medium">{row.model}</span> },
  { id: 'merchant_id', header: '商家', cell: (row) => <AdminNumber value={row.merchant_id} /> },
  { id: 'version', header: '版本', cell: (row) => <AdminNumber value={row.version} /> },
  { id: 'effective_at', header: '生效时刻', hideBelow: 'md', cell: (row) => <AdminTime value={row.effective_at} empty="—" /> },
  {
    id: 'retired_at',
    header: '退役时刻',
    hideBelow: 'lg',
    cell: (row) => <AdminTime value={row.retired_at ?? null} empty="—" />,
  },
  {
    id: 'status',
    header: '状态',
    cell: (row) => <AdminFlag on={row.retired_at === null} onText="生效中" offText="已退役" />,
  },
];

/** AdminPricing 渲染定价版本清单。 */
export function AdminPricing() {
  return (
    <AdminListPage<AdminPricing>
      title="价格"
      description="各商家、各模型的定价版本与生效状态。"
      columns={columns}
      load={listAdminPricing}
      filters={[
        { key: FilterMerchantID, label: '商家 id', placeholder: '正整数，如 1' },
        { key: FilterModel, label: '模型名', placeholder: '精确匹配，如 gpt-4o-mini' },
      ]}
      emptyText="还没有定价版本。发布一个版本后刷新即见。"
    />
  );
}
