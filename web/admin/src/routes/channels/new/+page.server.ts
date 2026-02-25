import { redirect, fail } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { Actions, PageServerLoad } from './$types';
import { AdminApiClient } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	// Load regions for the region multi-select (P14-T02).
	// If migration 022 has not been applied, return empty regions list.
	try {
		const res = await event.fetch(`${API_URL}/api/admin/regions`);
		if (res.ok) {
			const json = await res.json();
			return { regions: json.regions ?? [] };
		}
	} catch {
		// Regions table may not exist yet — degrade gracefully
	}
	return { regions: [] };
};

export const actions: Actions = {
	default: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();

		const name = (data.get('name') as string)?.trim();
		const slug = (data.get('slug') as string)?.trim();
		const category = (data.get('category') as string)?.trim();
		const stream_url = (data.get('stream_url') as string)?.trim();
		const logo_url = (data.get('logo_url') as string)?.trim() || null;
		const epg_id = (data.get('epg_id') as string)?.trim() || null;
		const sort_order = parseInt(data.get('sort_order') as string) || 0;
		const is_active = data.get('is_active') === 'on';

		if (!name || !slug || !category || !stream_url) {
			return fail(400, {
				error: 'Name, slug, category, and stream URL are required.',
				values: { name, slug, category, stream_url, logo_url, epg_id, sort_order }
			});
		}

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.createChannel({ name, slug, category, stream_url, logo_url, epg_id, sort_order, is_active });
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, {
				error: e.message ?? 'Failed to create channel.',
				values: { name, slug, category, stream_url, logo_url, epg_id, sort_order }
			});
		}

		redirect(302, '/channels');
	}
};
