import { redirect, fail } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { Actions, PageServerLoad } from './$types';
import { AdminApiClient, type Channel } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const client = new AdminApiClient(API_URL, event.locals.sessionToken);
	let channels: Channel[] = [];
	try {
		channels = await client.getChannels();
	} catch {
		// Return empty — UI shows empty state
	}

	return { channels };
};

export const actions: Actions = {
	delete: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();
		const id = data.get('id') as string;

		if (!id) return fail(400, { error: 'Channel ID required.' });

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.deleteChannel(id);
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { error: e.message ?? 'Failed to delete channel.' });
		}

		return { success: true };
	},

	toggleActive: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();
		const id = data.get('id') as string;
		const is_active = data.get('is_active') === 'true';

		if (!id) return fail(400, { error: 'Channel ID required.' });

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.updateChannel(id, { is_active: !is_active });
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { error: e.message ?? 'Failed to update channel.' });
		}

		return { success: true };
	}
};
