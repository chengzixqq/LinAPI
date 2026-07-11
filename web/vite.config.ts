import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// base=/console/ 与后端 console.go 的伺服前缀一致；
// 生产产物输出到 internal/server/web_dist 供 go:embed 打包。
// Mantine 样式走标准 CSS 导入（main.tsx 内 import '@mantine/core/styles.css'），
// PostCSS 由 postcss.config.cjs 驱动，无需再在此处做样式 alias。
export default defineConfig({
  plugins: [react()],
  base: '/console/',
  build: {
    outDir: '../internal/server/web_dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    // 字符串简写 = changeOrigin:false，Host 不被改写，后端 CSRF 同源校验才能通过。
    // 切勿改成 { target, changeOrigin: true } —— 会导致所有写请求 403（见 spec §3.6 CSRF）。
    proxy: {
      '/auth': 'http://localhost:8080',
      '/admin': 'http://localhost:8080',
      '/me': 'http://localhost:8080',
      '/v1': 'http://localhost:8080',
    },
  },
})
