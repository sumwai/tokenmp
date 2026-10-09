import {
  AdminFlag,
  AdminListPage,
  AdminText,
  AdminTime,
  type AdminColumn,
} from '../components/admin';
import { listAdminCredentials, type AdminCredential } from '../lib/admin';

/** 上游凭据清单页：分组、形态与脱敏前缀，明文不出现。 */

const columns: AdminColumn<AdminCredential>[] = [
  { id: 'cred_group', header: '分组', cell: (row) => <AdminText value={row.cred_group} /> },
  { id: 'name', header: '名称', hideBelow: 'md', cell: (row) => <AdminText value={row.name} /> },
  {
    id: 'kind',
    header: '形态',
    cell: (row) => <AdminText value={kindText(row.kind)} />,
  },
  {
    id: 'prefix',
    header: '前缀',
    cell: (row) => <code className="font-mono text-xs text-muted">{row.prefix}</code>,
  },
  {
    id: 'expires',
    header: '令牌有效期',
    hideBelow: 'lg',
    cell: (row) => <AdminTime value={row.expires ?? null} empty="—" />,
  },
  {
    id: 'expired',
    header: '令牌状态',
    hideBelow: 'lg',
    cell: (row) =>
      row.kind === 'oauth' ? (
        <AdminFlag on={!row.expired} onText="有效" offText="已过期" />
      ) : (
        <AdminText value="" />
      ),
  },
  {
    id: 'enabled',
    header: '状态',
    cell: (row) => <AdminFlag on={row.enabled} onText="已启用" offText="已停用" />,
  },
];

/** kindText 把凭据形态写成产品文案。 */
function kindText(kind: string): string {
  if (kind === 'api') return '接口密钥';
  if (kind === 'oauth') return 'OAuth 授权';
  return '无法识别';
}

/** AdminCredentials 渲染上游凭据清单。 */
export function AdminCredentials() {
  return (
    <AdminListPage<AdminCredential>
      title="凭据"
      description="上游凭据的分组、形态与脱敏前缀，明文不展示。"
      columns={columns}
      load={listAdminCredentials}
      emptyText="还没有凭据。写入一条凭据后刷新即见。"
    />
  );
}
