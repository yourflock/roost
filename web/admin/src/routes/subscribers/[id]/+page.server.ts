import { redirect, fail, error } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { Actions, PageServerLoad } from './$types';
import { AdminApiClient } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const { id } = event.params;
	const client = new AdminApiClient(API_URL, event.locals.sessionToken);

	try {
		const data = await client.getSubscriber(id);
		return data;
	} catch (err: unknown) {
		const e = err as { status?: number };
		if (e.status === 404) error(404, 'Subscriber not found.');
		error(500, 'Failed to load subscriber.');
	}
};

export const actions: Actions = {
	suspend: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const { id } = event.params;
		const data = await event.request.formData();
		const reason = (data.get('reason') as string)?.trim() || 'Suspended by admin';

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.suspendSubscriber(id, reason);
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { error: e.message ?? 'Failed to suspend subscriber.' });
		}

		return { success: true, action: 'suspended' };
	},

	reinstate: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const { id } = event.params;

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.reinstateSubscriber(id);
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { error: e.message ?? 'Failed to reinstate subscriber.' });
		}

		return { success: true, action: 'reinstated' };
	}
};
