import { Navigate, Route, Routes } from 'react-router-dom';

import { ConsoleLayout } from './components/console';
import { Account } from './pages/Account';
import { Callback } from './pages/Callback';
import { ForgotPassword } from './pages/ForgotPassword';
import { Home } from './pages/Home';
import { Keys } from './pages/Keys';
import { Login } from './pages/Login';
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
        <Route path="requests" element={<Requests />} />
        <Route path="usage" element={<Usage />} />
        <Route path="requests/:requestID" element={<RequestDetail />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
