import { Navigate, Route, Routes } from 'react-router-dom';

import { ForgotPassword } from './pages/ForgotPassword';
import { Home } from './pages/Home';
import { Login } from './pages/Login';
import { ResetPassword } from './pages/ResetPassword';
import { Signup } from './pages/Signup';

/** 路由表：认证四页公开，其余落到首页（首页自行做会话校验与回跳）。 */
export function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/signup" element={<Signup />} />
      <Route path="/forgot-password" element={<ForgotPassword />} />
      <Route path="/reset-password" element={<ResetPassword />} />
      <Route path="/" element={<Home />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
