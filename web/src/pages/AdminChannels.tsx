import {
  AdminFlag,
  AdminListPage,
  AdminNumber,
  AdminText,
  type AdminColumn,
} from '../components/admin';
import { listAdminChannels, type AdminChannel } from '../lib/admin';

/** 渠道清单页：上游渠道的地址、协议与启用状态。 */

const columns: AdminColumn<AdminChannel>[] = [
  { id: 'name', header: '渠道', cell: (row) => <span className="font-medium">{row.name}</span> },
  { id: 'vendor', header: '厂商', hideBelow: 'md', cell: (row) => <AdminText value={row.vendor} /> },
  {
    id: 'type',
    header: '协议',
    hideBelow: 'md',
    cell: (row) => <code className="font-mono text-xs text-muted">{row.type}</code>,
  },
  { id: 'cred_group', header: '凭据分组', hideBelow: 'lg', cell: (row) => <AdminText value={row.cred_group} /> },
  {
    id: 'base_url',
    header: '上游地址',
    hideBelow: 'lg',
    cell: (row) => <code className="font-mono text-xs text-muted">{row.base_url}</code>,
  },
  { id: 'priority', header: '优先级', hideBelow: 'lg', cell: (row) => <AdminNumber value={row.priority} /> },
  { id: 'weight', header: '权重', hideBelow: 'lg', cell: (row) => <AdminNumber value={row.weight} /> },
  {
    id: 'enabled',
    header: '状态',
    cell: (row) => <AdminFlag on={row.enabled} onText="已启用" offText="已停用" />,
  },
];

/** AdminChannels 渲染渠道清单。 */
export function AdminChannels() {
  return (
    <AdminListPage<AdminChannel>
      title="渠道"
      description="上游渠道的地址、协议与启用状态。"
      columns={columns}
      load={listAdminChannels}
      emptyText="还没有渠道。写入一条渠道后刷新即见。"
    />
  );
}
