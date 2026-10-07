import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// 开发期把 /api 代理到本地网关：页面契约要求同源，
// 跨域形态（分离域名 + CORS）不作为开发路径，避免两套行为分叉。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
});
