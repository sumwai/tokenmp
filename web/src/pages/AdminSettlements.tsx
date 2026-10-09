import { AdminDecimal, AdminListPage, AdminNumber, AdminTime, type AdminColumn } from '../components/admin';
import {
  FilterFrom,
  FilterMerchantID,
  FilterTo,
  listAdminSettlements,
  type AdminSettlementItem,
} from '../lib/admin';
import { SETTLEMENT_PERIOD_LABELS } from '../lib/partner';

/** 全平台结算对账单页：按商家逐行列出各账期的分账结果。 */

const columns: AdminColumn<AdminSettlementItem>[] = [
  { id: 'merchant_id', header: '商家', cell: (row) => <AdminNumber value={row.merchant_id} /> },
  {
    id: 'period',
    header: '账期',
    cell: (row) => <span>按{SETTLEMENT_PERIOD_LABELS[row.period] ?? row.period}</span>,
  },
  { id: 'from', header: '起', hideBelow: 'md', cell: (row) => <AdminTime value={row.from} empty="—" /> },
  { id: 'to', header: '止', hideBelow: 'md', cell: (row) => <AdminTime value={row.to} empty="—" /> },
  { id: 'trades', header: '笔数', cell: (row) => <AdminNumber value={row.trades} /> },
  { id: 'gross_sales', header: '卖出总额', cell: (row) => <AdminDecimal value={row.gross_sales} /> },
  { id: 'commission', header: '平台抽成', cell: (row) => <AdminDecimal value={row.commission} /> },
  {
    id: 'upstream_cost',
    header: '上游成本',
    hideBelow: 'lg',
    cell: (row) => <AdminDecimal value={row.upstream_cost} />,
  },
  { id: 'payout', header: '商家收益', cell: (row) => <AdminDecimal value={row.payout} /> },
];

/** AdminSettlements 渲染全平台结算对账单。 */
export function AdminSettlements() {
  return (
    <AdminListPage<AdminSettlementItem>
      title="结算"
      description="按商家逐行列出分账对账单；账期缺省时取各商家账期的上一个完整自然周期。"
      columns={columns}
      load={listAdminSettlements}
      filters={[
        { key: FilterMerchantID, label: '商家 id', placeholder: '正整数，留空列出全部' },
        { key: FilterFrom, label: '账期起点', placeholder: 'RFC3339，如 2026-09-01T00:00:00Z' },
        { key: FilterTo, label: '账期终点', placeholder: 'RFC3339，与起点成对给出' },
      ]}
      emptyText="该条件下没有结算对账单。放宽条件或换一个账期后再看。"
    />
  );
}
