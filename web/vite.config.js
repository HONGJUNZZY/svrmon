import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { readdir, readFile, writeFile } from 'node:fs/promises'
import { resolve, extname } from 'node:path'
import { gzipSync } from 'node:zlib'

function compressedAssets() {
  let outputDir
  return {
    name: 'compressed-assets',
    apply: 'build',
    configResolved(config) {
      outputDir = resolve(config.root, config.build.outDir)
    },
    async closeBundle() {
      async function walk(dir) {
        for (const entry of await readdir(dir, { withFileTypes: true })) {
          const path = resolve(dir, entry.name)
          if (entry.isDirectory()) await walk(path)
          else if (['.html', '.js', '.css', '.svg'].includes(extname(path))) {
            const data = await readFile(path)
            const compressed = gzipSync(data, { level: 9 })
            if (compressed.length < data.length) await writeFile(path + '.gz', compressed)
          }
        }
      }
      await walk(outputDir)
    },
  }
}

export default defineConfig({
  plugins: [vue(), compressedAssets()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/ws': { target: 'ws://127.0.0.1:8080', ws: true },
    },
  },
  build: { outDir: 'dist', chunkSizeWarningLimit: 1500 },
})
