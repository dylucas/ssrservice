import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'

// outDir mirrors internal/webui/dist so the Go binary can embed the build.
const outDir = fileURLToPath(new URL('../internal/webui/dist', import.meta.url))

// keepEmbedPlaceholder re-creates dist/.gitkeep after a build. Vite empties the
// output directory, and that placeholder is tracked so a checkout without a
// frontend build still satisfies the //go:embed all:dist directive. Without
// this, running any build would show up as a spurious deletion in git.
function keepEmbedPlaceholder(): Plugin {
  return {
    name: 'sspanel-keep-embed-placeholder',
    closeBundle() {
      mkdirSync(outDir, { recursive: true })
      writeFileSync(join(outDir, '.gitkeep'), '')
    },
  }
}

export default defineConfig({
  plugins: [vue(), keepEmbedPlaceholder()],
  base: './',
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir,
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:2096',
      '/sub': 'http://127.0.0.1:2096',
    },
  },
})
