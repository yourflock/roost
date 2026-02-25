import type { Config } from 'tailwindcss';

export default {
	content: ['./src/**/*.{html,js,svelte,ts}'],
	theme: {
		extend: {
			colors: {
				roost: {
					400: '#f97316',
					500: '#ea580c',
					600: '#c2410c',
					700: '#9a3412'
				}
			}
		}
	},
	plugins: []
} satisfies Config;
