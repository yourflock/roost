import { redirect } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';
import { SESSION_COOKIE } from '$lib/server/auth';
import type { Subscription, Invoice } from '$lib/api';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	const { subscriber } = await event.parent();
	if (!subscriber) throw redirect(303, '/login');

	const token = event.cookies.get(SESSION_COOKIE) ?? '';
	const headers = { Authorization: `Bearer ${token}` };

	const [subRes, invoiceRes] = await Promise.allSettled([
		fetch(`${API_URL}/billing/subscription`, { headers }),
		fetch(`${API_URL}/billing/invoices`, { headers })
	]);

	let subscription: Subscription | null = null;
	let invoices: Invoice[] = [];

	if (subRes.status === 'fulfilled' && subRes.value.ok) {
		subscription = await subRes.value.json();
	}
	if (invoiceRes.status === 'fulfilled' && invoiceRes.value.ok) {
		invoices = await invoiceRes.value.json();
	}

	return { subscriber, subscription, invoices };
};
