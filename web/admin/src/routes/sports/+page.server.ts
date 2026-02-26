// +page.server.ts — Sports dashboard server loader.
import { redirect } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { PageServerLoad } from './$types';

const SPORTS_API_URL = env.ROOST_SPORTS_API_URL ?? 'http://localhost:8102';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	let leagues: unknown[] = [];
	let liveCount = 0;
	let upcomingCount = 0;
	let teamCount = 0;

	try {
		const [leaguesRes, liveRes] = await Promise.allSettled([
			fetch(`${SPORTS_API_URL}/sports/leagues`),
			fetch(`${SPORTS_API_URL}/sports/live`)
		]);

		if (leaguesRes.status === 'fulfilled' && leaguesRes.value.ok) {
			const data = await leaguesRes.value.json();
			leagues = data.leagues ?? [];
			// Count teams across all leagues
			for (const league of leagues as Array<{ id: string }>) {
				try {
					const teamsRes = await fetch(`${SPORTS_API_URL}/sports/leagues/${league.id}/teams`);
					if (teamsRes.ok) {
						const teamsData = await teamsRes.json();
						teamCount += (teamsData.teams ?? []).length;
					}
				} catch {
					// ignore
				}
			}
		}

		if (liveRes.status === 'fulfilled' && liveRes.value.ok) {
			const data = await liveRes.value.json();
			liveCount = data.count ?? 0;
		}

		// Upcoming this week
		const eventsRes = await fetch(`${SPORTS_API_URL}/sports/events?status=scheduled`);
		if (eventsRes.ok) {
			const data = await eventsRes.json();
			const events = (data.events ?? []) as Array<{ scheduled_time: string }>;
			const weekFromNow = Date.now() + 7 * 24 * 60 * 60 * 1000;
			upcomingCount = events.filter(
				(e) => new Date(e.scheduled_time).getTime() <= weekFromNow
			).length;
		}
	} catch {
		// Return partial data — UI handles gracefully
	}

	return { leagues, liveCount, upcomingCount, teamCount, leagueCount: leagues.length };
};
