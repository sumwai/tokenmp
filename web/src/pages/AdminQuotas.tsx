import { AdminDecimal, AdminListPage, AdminText, type AdminColumn } from '../components/admin';
import {
  FilterAccountID,
  FilterScope,
  FilterScopeID,
  listAdminQuotas,
  type AdminQuota,
} from '../lib/admin';

/** 窗口限额清单页：定义加当前窗口的已用量与剩余额度。 */

const columns: AdminColumn<AdminQuota>[] = [
  {
    id: 'scope',
    header: '范围',
    cell: (row) => (
      <span>
        <AdminText value={scopeText(row.scope)} /> <span className="tabular-nums">#{row.scope_id}</span>
      </span>
    ),
  },
  { id: 'metric', header: '指标', cell: (row) => <AdminText value={row.metric} /> },
  {
    id: 'window',
    header: '窗口',
    hideBelow: 'md',
    cell: (row) => <AdminText value={`${row.window_kind} / ${row.period}`} />,
  },
  {
    id: 'limit_amount',
    header: '上限',
    cell: (row) => <AdminDecimal value={row.limit_amount} />,
  },
  {
    id: 'used',
    header: '已用',
    hideBelow: 'md',
    cell: (row) => <AdminText value={row.used ?? ''} />,
  },
  {
    id: 'remaining',
    header: '剩余',
    hideBelow: 'md',
    cell: (row) => <AdminText value={row.remaining ?? ''} />,
  },
  { id: 'action', header: '超限处置', hideBelow: 'lg', cell: (row) => <AdminText value={row.action} /> },
];

/** scopeText 把限额范围写成产品文案。 */
function scopeText(scope: string): string {
  if (scope === 'account') return '账户';
  if (scope === 'api_key') return '密钥';
  if (scope === 'channel') return '渠道';
  if (scope === 'plan') return '套餐';
  return scope;
}

/** AdminQuotas 渲染窗口限额清单。 */
export function AdminQuotas() {
  return (
    <AdminListPage<AdminQuota>
      title="限额"
      description="窗口限额的定义与当前窗口的已用量、剩余额度。"
      columns={columns}
      load={listAdminQuotas}
      filters={[
        { key: FilterScope, label: '范围', placeholder: 'account / api_key / channel / plan' },
        { key: FilterScopeID, label: '范围实体 id', placeholder: '与范围成对使用' },
        { key: FilterAccountID, label: '账户 id', placeholder: '账户维度的简写' },
      ]}
      emptyText="还没有限额定义。新增一条限额后刷新即见。"
    />
  );
}
