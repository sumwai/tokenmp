import { Navigate, Route, Routes } from 'react-router-dom';

import { ConsoleLayout } from './components/console';
import { Account } from './pages/Account';
import { AdminAccounts } from './pages/AdminAccounts';
import { AdminAdjustments } from './pages/AdminAdjustments';
import { AdminChannels } from './pages/AdminChannels';
import { AdminCredentials } from './pages/AdminCredentials';
import { AdminModelMaps } from './pages/AdminModelMaps';
import { AdminPricing } from './pages/AdminPricing';
import { AdminQuotas } from './pages/AdminQuotas';
import { AdminUsage } from './pages/AdminUsage';
import { Callback } from './pages/Callback';
import { ForgotPassword } from './pages/ForgotPassword';
import { Home } from './pages/Home';
import { Keys } from './pages/Keys';
import { Login } from './pages/Login';
import { Purchase } from './pages/Purchase';
import { RequestDetail } from './pages/RequestDetail';
import { Requests } from './pages/Requests';
import { ResetPassword } from './pages/ResetPassword';
import { Signup } from './pages/Signup';
import { Usage } from './pages/Usage';

/**
 * 路由表：认证五页公开，其余路径都落在控制台骨架内。
 *
 * 骨架承担会话校验、登录回跳、导航与无权访问兜底，页面只按清单渲染内容；
 * 控制台内未声明的路径回首页。
 */
export function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/signup" element={<Signup />} />
      <Route path="/forgot-password" element={<ForgotPassword />} />
      <Route path="/reset-password" element={<ResetPassword />} />
      <Route path="/auth/callback" element={<Callback />} />
      <Route element={<ConsoleLayout />}>
        <Route index element={<Home />} />
        <Route path="account" element={<Account />} />
        <Route path="keys" element={<Keys />} />
        <Route path="purchase" element={<Purchase />} />
        <Route path="requests" element={<Requests />} />
        <Route path="usage" element={<Usage />} />
        <Route path="requests/:requestID" element={<RequestDetail />} />
        <Route path="admin/channels" element={<AdminChannels />} />
        <Route path="admin/credentials" element={<AdminCredentials />} />
        <Route path="admin/modelmaps" element={<AdminModelMaps />} />
        <Route path="admin/accounts" element={<AdminAccounts />} />
        <Route path="admin/pricing" element={<AdminPricing />} />
        <Route path="admin/quotas" element={<AdminQuotas />} />
        <Route path="admin/adjustments" element={<AdminAdjustments />} />
        <Route path="admin/usage" element={<AdminUsage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
