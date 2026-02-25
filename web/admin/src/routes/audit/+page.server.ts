// +page.server.ts — Audit log page data loader.
// P16-T01: Structured Logging & Audit Trail
import { redirect } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { PageServerLoad } from './$types';
import { AdminApiClient } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const url = event.url;
	const actor_id = url.searchParams.get('actor_id') ?? '';
	const action = url.searchParams.get('action') ?? '';
	const resource_type = url.searchParams.get('resource_type') ?? '';
	const date_from = url.searchParams.get('date_from') ?? '';
	const date_to = url.searchParams.get('date_to') ?? '';
	const page = parseInt(url.searchParams.get('page') ?? '1');

	const client = new AdminApiClient(API_URL, event.locals.sessionToken);

	let result = { entries: [], total: 0, page: 1, per_page: 50, total_pages: 1 };
	try {
		const params = new URLSearchParams({ page: String(page), per_page: '50' });
		if (actor_id) params.set('actor_id', actor_id);
		if (action) params.set('action', action);
		if (resource_type) params.set('resource_type', resource_type);
		if (date_from) params.set('date_from', date_from);
		if (date_to) params.set('date_to', date_to);
		result = await (client as any).request('GET', `/admin/audit?${params}`);
	} catch {
		// Return empty
	}

	return { ...result, filters: { actor_id, action, resource_type, date_from, date_to } };
};
