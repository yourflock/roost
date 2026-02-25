import { redirect, fail } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { Actions, PageServerLoad } from './$types';
import { AdminApiClient, type ActiveStream } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const client = new AdminApiClient(API_URL, event.locals.sessionToken);
	let streams: ActiveStream[] = [];
	try {
		streams = await client.getActiveStreams();
	} catch {
		// Return empty
	}

	return { streams };
};

export const actions: Actions = {
	terminate: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();
		const streamId = data.get('stream_id') as string;

		if (!streamId) return fail(400, { error: 'Stream ID required.' });

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.terminateStream(streamId);
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { error: e.message ?? 'Failed to terminate stream.' });
		}

		return { success: true };
	}
};
