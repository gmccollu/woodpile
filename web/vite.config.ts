import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// Vite empties dist/ before writing. The Go side embeds web/dist with //go:embed,
// which needs at least one file there, and git tracks dist/.keep, so put it back.
function keepFile(): Plugin {
  return {
    name: 'woodpile-keep-dist',
    apply: 'build',
    closeBundle() {
      writeFileSync(fileURLToPath(new URL('./dist/.keep', import.meta.url)), '');
    },
  };
}

export default defineConfig({
  base: './',
  plugins: [tailwindcss(), svelte(), keepFile()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    assetsInlineLimit: 0,
    reportCompressedSize: true,
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    // Without mock mode, talk to a locally running woodpile.
    proxy: { '/api': { target: 'http://127.0.0.1:8514', changeOrigin: false } },
  },
});
