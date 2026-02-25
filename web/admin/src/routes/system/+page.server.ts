import { redirect } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { PageServerLoad } from './$types';
import { AdminApiClient } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const client = new AdminApiClient(API_URL, event.locals.sessionToken);
	let services: { name: string; status: string; latency_ms: number | null; details: string | null; checked_at: string }[] = [];

	try {
		services = await client.getServiceHealth();
	} catch {
		// Return empty
	}

	return { services, checkedAt: new Date().toISOString() };
};
