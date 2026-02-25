import { redirect } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { PageServerLoad } from './$types';
import { AdminApiClient, type Subscriber } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const url = event.url;
	const search = url.searchParams.get('search') ?? '';
	const plan = url.searchParams.get('plan') ?? '';
	const status = url.searchParams.get('status') ?? '';
	const page = parseInt(url.searchParams.get('page') ?? '1');

	const client = new AdminApiClient(API_URL, event.locals.sessionToken);
	let result: { subscribers: Subscriber[]; total: number; page: number; per_page: number } = { subscribers: [], total: 0, page: 1, per_page: 25 };
	try {
		result = await client.getSubscribers({ search, plan, status, page, per_page: 25 });
	} catch {
		// Return empty
	}

	return { ...result, search, plan, status };
};
