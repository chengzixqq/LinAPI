import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

// base=/console/ 与后端 console.go 的伺服前缀一致；
// 生产产物输出到 internal/server/web_dist 供 go:embed 打包。
// Mantine 样式走标准 CSS 导入（main.tsx 内 import '@mantine/core/styles.css'），
// PostCSS 由 postcss.config.cjs 驱动，无需再在此处做样式 alias。

// keepGitkeep 在每次构建收尾重建 web_dist/.gitkeep：emptyOutDir 会清空目录连带删掉
// 这个被 git 追踪的占位文件，而 //go:embed all:web_dist 要求目录在编译期存在——
// 全新 checkout 若丢了占位文件会导致 go build 失败。构建后补回，让产物目录永远可编译。
function keepGitkeep() {
  return {
    name: 'linapi-keep-gitkeep',
    closeBundle() {
      writeFileSync(resolve(__dirname, '../internal/server/web_dist/.gitkeep'), '')
    },
  }
}

export default defineConfig({
  plugins: [react(), keepGitkeep()],
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
