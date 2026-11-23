import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// workspace.html is the workspace playground. It runs against fake data
// beside the existing app until the workspace can host the real terminal.
export default defineConfig({
  plugins: [svelte()],
  base: './',
  build: { rollupOptions: { input: { main: 'index.html', workspace: 'workspace.html' } } },
});
