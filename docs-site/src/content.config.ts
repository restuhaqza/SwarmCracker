import { defineCollection } from 'astro:content';
import { docsLoader, i18nLoader } from '@astrojs/starlight/loaders';
import { docsSchema, i18nSchema } from '@astrojs/starlight/schema';

export const collections = {
  docs: defineCollection({ loader: docsLoader(), schema: docsSchema() }),
  // Optional UI-translation collection. Kept defined (with an empty `en` file)
  // so Starlight's translation system resolves cleanly without warnings.
  i18n: defineCollection({ loader: i18nLoader(), schema: i18nSchema() }),
};
