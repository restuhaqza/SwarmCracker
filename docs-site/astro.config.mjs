// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';

// https://astro.build/config
export default defineConfig({
  site: 'https://docs.swarmcracker.com',
  integrations: [
    mermaid({
      theme: 'default',
      autoTheme: true,
      enableLog: false,
    }),
    starlight({
      title: 'SwarmCracker',
      description:
        'Run containers as hardware-isolated Firecracker microVMs with the SwarmKit orchestration you already know.',
      logo: {
        src: './src/assets/logo.svg',
        alt: 'SwarmCracker',
      },
      favicon: '/favicon.svg',
      social: [
        {
          icon: 'github',
          label: 'GitHub',
          href: 'https://github.com/restuhaqza/SwarmCracker',
        },
      ],
      editLink: {
        baseUrl:
          'https://github.com/restuhaqza/SwarmCracker/edit/main/docs-site/src/content/docs/',
      },
      lastUpdated: true,
      pagination: true,
      customCss: ['./src/styles/custom.css'],
      head: [
        {
          tag: 'meta',
          attrs: {
            name: 'og:type',
            content: 'website',
          },
        },
      ],
      sidebar: [
        {
          label: 'Getting Started',
          items: [{ slug: 'getting-started' }],
        },
        {
          label: 'Guides',
          items: [{ autogenerate: { directory: 'guides' } }],
        },
        {
          label: 'Architecture',
          items: [{ autogenerate: { directory: 'architecture' } }],
        },
        {
          label: 'Reference',
          items: [{ autogenerate: { directory: 'reference' } }],
        },
        {
          label: 'Contributing',
          items: [
            { slug: 'contributing' },
            { slug: 'contributing/guidelines' },
            { slug: 'contributing/security' },
            {
              label: 'Testing',
              items: [{ autogenerate: { directory: 'contributing/testing' } }],
            },
            {
              label: 'Package Reference',
              collapsed: true,
              items: [
                { autogenerate: { directory: 'contributing/reference' } },
              ],
            },
          ],
        },
      ],
    }),
  ],
});
