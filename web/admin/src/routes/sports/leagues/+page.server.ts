// +page.server.ts — Sports leagues list server loader.
import { redirect } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { PageServerLoad, Actions } from './$types';

const SPORTS_API_URL = env.ROOST_SPORTS_API_URL ?? 'http://localhost:8102';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	let leagues: unknown[] = [];
	try {
		const res = await fetch(`${SPORTS_API_URL}/sports/leagues`);
		if (res.ok) {
			const data = await res.json();
			leagues = data.leagues ?? [];
		}
	} catch {
		// pass
	}
	return { leagues };
};

export const actions: Actions = {
	sync: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		try {
			await fetch(`${SPORTS_API_URL}/admin/sports/sync`, { method: 'POST' });
			return { success: true, message: 'Sync triggered' };
		} catch {
			return { success: false, message: 'Failed to trigger sync' };
		}
	}
};
