import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://swarmcracker.com',
  output: 'static',
  build: { format: 'directory' },
});
