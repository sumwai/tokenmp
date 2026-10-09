import {
  AdminDecimal,
  AdminFlag,
  AdminListPage,
  AdminNumber,
  AdminText,
  type AdminColumn,
} from '../components/admin';
import { listAdminModelMaps, type AdminModelMap } from '../lib/admin';

/** 渠道模型映射清单页：模型替换与价格倍率。 */

const columns: AdminColumn<AdminModelMap>[] = [
  { id: 'model', header: '模型', cell: (row) => <span className="font-medium">{row.model}</span> },
  {
    id: 'upstream_model',
    header: '上游模型',
    cell: (row) => <AdminText value={row.upstream_model} />,
  },
  { id: 'channel_id', header: '渠道', hideBelow: 'md', cell: (row) => <AdminNumber value={row.channel_id} /> },
  {
    id: 'price_multiplier',
    header: '价格倍率',
    cell: (row) => <AdminDecimal value={row.price_multiplier} />,
  },
  {
    id: 'request_overrides',
    header: '请求覆盖',
    hideBelow: 'lg',
    cell: (row) => (
      <code className="block max-w-[16rem] truncate font-mono text-xs text-muted">
        {row.request_overrides ? JSON.stringify(row.request_overrides) : '—'}
      </code>
    ),
  },
  {
    id: 'enabled',
    header: '状态',
    cell: (row) => <AdminFlag on={row.enabled} onText="已启用" offText="已停用" />,
  },
];

/** AdminModelMaps 渲染渠道模型映射清单。 */
export function AdminModelMaps() {
  return (
    <AdminListPage<AdminModelMap>
      title="模型映射"
      description="渠道上的模型替换、价格倍率与请求覆盖。"
      columns={columns}
      load={listAdminModelMaps}
      emptyText="还没有模型映射。写入一条映射后刷新即见。"
    />
  );
}
