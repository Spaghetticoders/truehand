import { defineConfig, loadEnv, type Plugin } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

const normalize = (root: string, p: string) => {
  const out: string[] = []
  for (const part of (p.startsWith('/') ? p : `${root}/${p}`).split('/')) {
    if (part === '..') out.pop()
    else if (part && part !== '.') out.push(part)
  }
  return '/' + out.join('/')
}

// dist is embedded in the binary: it must never carry the mock data.
function noFixtureInDist(mock: boolean): Plugin {
  return {
    name: 'truehand:no-fixture-in-dist',
    apply: 'build',
    configResolved(c) {
      if (mock && normalize(c.root, c.build.outDir) === normalize(c.root, 'dist')) throw new Error('mock build into dist: use bun run build:mock (dist-mock)')
    },
    generateBundle(_, bundle) {
      if (mock) return
      for (const chunk of Object.values(bundle)) {
        if (chunk.type === 'chunk' && Object.keys(chunk.modules).some((id) => id.includes('/lib/api/fixture/'))) {
          this.error(`fixture code in ${chunk.fileName}: the real build must not ship the mock`)
        }
      }
    },
  }
}

export default defineConfig(({ mode }) => {
  const mock = loadEnv(mode, '.', 'VITE_').VITE_TRUEHAND_API === 'mock'
  return {
    plugins: [svelte(), noFixtureInDist(mock)],
    base: './',
    build: { outDir: mock ? 'dist-mock' : 'dist', emptyOutDir: true, chunkSizeWarningLimit: 800 },
  }
})
