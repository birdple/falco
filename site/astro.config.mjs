// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';

const GITHUB = 'https://github.com/birdple/falco';

export default defineConfig({
	site: 'https://birdple.github.io',
	base: '/falco',
	// Astro's HTML compressor strips the whitespace between text and inline tags,
	// which glues prose to <code> and <a> ("libvips'spkg-config", "does.The full
	// table").
	compressHTML: false,
	trailingSlash: 'always',
	integrations: [
		starlight({
			title: 'Falco',
			description:
				'A self-hosted image service in Go: store images, transform them on the way out, and serve them signed. libvips for the pixels, S3, R2, Jay or the filesystem for the bytes.',
			social: [{ icon: 'github', label: 'GitHub', href: GITHUB }],
			editLink: { baseUrl: `${GITHUB}/edit/main/site/` },
			lastUpdated: true,
			favicon: '/favicon.svg',
			customCss: [
				'@fontsource/ibm-plex-sans/latin-400.css',
				'@fontsource/ibm-plex-sans/latin-600.css',
				'@fontsource/ibm-plex-mono/latin-400.css',
				'@fontsource/ibm-plex-mono/latin-500.css',
				'./src/styles/theme.css',
			],
			plugins: [starlightLinksValidator({ errorOnRelativeLinks: false })],
			sidebar: [
				{
					label: 'Start here',
					items: [
						{ label: 'What Falco is', slug: 'what-falco-is' },
						{ label: 'Quickstart', slug: 'quickstart' },
						{ label: 'Install', slug: 'install' },
					],
				},
				{
					label: 'Guides',
					items: [
						{ label: 'Uploading', slug: 'guides/uploading' },
						{ label: 'Delivering images', slug: 'guides/delivering' },
						{ label: 'Signed URLs', slug: 'guides/signed-urls' },
						{ label: 'Watermarks', slug: 'guides/watermarks' },
						{ label: 'Buckets and groups', slug: 'guides/buckets' },
						{ label: 'Backups', slug: 'guides/backups' },
						{ label: 'Proxying external images', slug: 'guides/proxy' },
						{ label: 'Deploying Falco', slug: 'guides/deployment' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'Configuration', slug: 'reference/configuration' },
						{ label: 'HTTP API', slug: 'reference/http-api' },
						{ label: 'Transformations', slug: 'reference/transformations' },
						{ label: 'Authentication', slug: 'reference/authentication' },
						{ label: 'Observability', slug: 'reference/observability' },
					],
				},
				{
					label: 'Under the hood',
					items: [
						{ label: 'Architecture', slug: 'internals/architecture' },
						{ label: 'Caching', slug: 'internals/caching' },
						{ label: 'Falco vs imgproxy', slug: 'internals/vs-imgproxy' },
					],
				},
			],
		}),
	],
});
