import { redirect, fail } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { Actions, PageServerLoad } from './$types';
import { AdminApiClient, type EpgSource } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const client = new AdminApiClient(API_URL, event.locals.sessionToken);
	let sources: EpgSource[] = [];
	try {
		sources = await client.getEpgSources();
	} catch {
		// Return empty
	}

	return { sources };
};

export const actions: Actions = {
	add: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();

		const name = (data.get('name') as string)?.trim();
		const url = (data.get('url') as string)?.trim();
		const format = data.get('format') as 'xmltv' | 'm3u';

		if (!name || !url || !format) {
			return fail(400, { addError: 'Name, URL, and format are required.' });
		}

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.addEpgSource({ name, url, format });
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { addError: e.message ?? 'Failed to add EPG source.' });
		}

		return { addSuccess: true };
	},

	remove: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();
		const id = data.get('id') as string;

		if (!id) return fail(400, { addError: 'Source ID required.' });

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.removeEpgSource(id);
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { addError: e.message ?? 'Failed to remove EPG source.' });
		}

		return { addSuccess: true };
	},

	sync: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();
		const id = data.get('id') as string;

		if (!id) return fail(400, { addError: 'Source ID required.' });

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.triggerEpgSync(id);
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { addError: e.message ?? 'Failed to trigger EPG sync.' });
		}

		return { syncTriggered: true };
	}
};
