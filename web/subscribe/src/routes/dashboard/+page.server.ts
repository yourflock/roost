import { redirect } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';
import { SESSION_COOKIE } from '$lib/server/auth';
import type { Subscription, ApiToken } from '$lib/api';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	const { subscriber } = await event.parent();
	if (!subscriber) throw redirect(303, '/login');

	const token = event.cookies.get(SESSION_COOKIE) ?? '';
	const headers = { Authorization: `Bearer ${token}` };

	// Fetch subscription status and API token in parallel
	const [subscriptionRes, apiTokenRes] = await Promise.allSettled([
		fetch(`${API_URL}/billing/subscription`, { headers }),
		fetch(`${API_URL}/billing/api-token`, { headers })
	]);

	let subscription: Subscription | null = null;
	let apiToken: ApiToken | null = null;

	if (subscriptionRes.status === 'fulfilled' && subscriptionRes.value.ok) {
		subscription = await subscriptionRes.value.json();
	}

	if (apiTokenRes.status === 'fulfilled' && apiTokenRes.value.ok) {
		apiToken = await apiTokenRes.value.json();
	}

	return { subscriber, subscription, apiToken };
};
