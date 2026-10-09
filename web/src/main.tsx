import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

import { App } from './App';
import './styles.css';

/**
 * 请求层配置：失败不自动重试。
 *
 * web/AGENTS.md 的错误态要求「5xx 显示错误页与手动重试按钮，不自动循环重试」，
 * 重试只由页面的按钮触发，客户端不做隐式重放。
 */
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false },
    mutations: { retry: false },
  },
});

const root = document.getElementById('root');
if (!root) {
  throw new Error('缺少 #root 挂载点');
}

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
