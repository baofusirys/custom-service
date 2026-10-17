import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  base: '/admin/',
  build: {
    target: 'es2018',
    cssCodeSplit: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          vendor: ['vue', 'vue-router', 'pinia'],
          elp: ['element-plus'],
          // [119] 多人大厅媒体 SDK 单独分包：只在进入大厅页时按需加载，且大厅页代码更新不影响其长缓存
          livekit: ['livekit-client']
        }
      }
    }
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true }
    }
  }
})
