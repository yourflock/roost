// +page.server.ts — Sports events list server loader.
import { redirect } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { PageServerLoad } from './$types';

const SPORTS_API_URL = env.ROOST_SPORTS_API_URL ?? 'http://localhost:8102';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const url = event.url;
	const league = url.searchParams.get('league') ?? '';
	const status = url.searchParams.get('status') ?? '';

	let events: unknown[] = [];
	try {
		const params = new URLSearchParams();
		if (league) params.set('league', league);
		if (status) params.set('status', status);
		const res = await fetch(`${SPORTS_API_URL}/sports/events?${params}`);
		if (res.ok) {
			const data = await res.json();
			events = data.events ?? [];
		}
	} catch {
		// pass
	}

	// Fetch leagues for filter dropdown
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

	return { events, leagues, filterLeague: league, filterStatus: status };
};
