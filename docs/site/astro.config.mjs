// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';

const repo = 'https://github.com/Mark7888/two-place-paste';

// Published to GitHub Pages as a project site, so every page lives under
// /two-place-paste/. Links inside pages must include that prefix.
export default defineConfig({
	site: 'https://mark7888.github.io',
	base: '/two-place-paste',
	integrations: [
		starlight({
			title: 'TwoPlacePaste',
			description:
				'A self-hosted, end-to-end encrypted shared clipboard. Copy on one device, paste on another.',
			logo: { src: './src/assets/logo.svg' },
			favicon: '/favicon.svg',
			social: [{ icon: 'github', label: 'GitHub', href: repo }],
			editLink: { baseUrl: `${repo}/edit/master/docs/site/` },
			lastUpdated: true,
			customCss: ['./src/styles/custom.css'],
			plugins: [starlightLinksValidator()],
			sidebar: [
				{
					label: 'Start here',
					items: ['getting-started', 'how-it-works'],
				},
				{
					label: 'Install the app',
					items: [
						'install/windows',
						'install/macos',
						'install/linux',
						'install/android',
						'install/updates',
						'install/uninstall',
					],
				},
				{
					label: 'Using TwoPlacePaste',
					items: [
						'guides/create-a-group',
						'guides/add-a-device',
						'guides/sync',
						'guides/tray',
						'guides/history',
						'guides/store',
						'guides/devices',
						'guides/settings',
						'guides/troubleshooting',
					],
				},
				{
					label: 'Run your own server',
					items: [
						'self-hosting/overview',
						'self-hosting/docker',
						'self-hosting/from-source',
						'self-hosting/reverse-proxy',
						'self-hosting/configuration',
						'self-hosting/admin',
						'self-hosting/blob-storage',
						'self-hosting/operations',
					],
				},
				{
					label: 'Reference',
					items: ['security', 'faq'],
				},
				{
					label: 'Contributing',
					items: ['contributing/development', 'contributing/repository'],
				},
			],
		}),
	],
});
