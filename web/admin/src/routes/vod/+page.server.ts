// +page.server.ts — VOD catalog admin
// Fetches VOD content from the vod service (port 8097) and handles CRUD actions.

import { fail } from '@sveltejs/kit';
import type { PageServerLoad, Actions } from './$types';

const VOD_SERVICE = process.env.VOD_SERVICE_URL ?? 'http://localhost:8097';

export const load: PageServerLoad = async ({ cookies }) => {
	const token = cookies.get('admin_token') ?? '';

	const [moviesRes, seriesRes] = await Promise.all([
		fetch(`${VOD_SERVICE}/admin/vod/movies?limit=200`, {
			headers: { Authorization: `Bearer ${token}` }
		}).catch(() => null),
		fetch(`${VOD_SERVICE}/admin/vod/series?limit=200`, {
			headers: { Authorization: `Bearer ${token}` }
		}).catch(() => null)
	]);

	const movies = moviesRes?.ok ? ((await moviesRes.json()).items ?? []) : [];
	const series = seriesRes?.ok ? ((await seriesRes.json()).items ?? []) : [];

	return { movies, series };
};

export const actions: Actions = {
	deleteMovie: async ({ request, cookies }) => {
		const token = cookies.get('admin_token') ?? '';
		const data = await request.formData();
		const id = data.get('id') as string;
		if (!id) return fail(400, { error: 'ID required' });

		const res = await fetch(`${VOD_SERVICE}/admin/vod/movies/${id}`, {
			method: 'DELETE',
			headers: { Authorization: `Bearer ${token}` }
		});
		if (!res.ok) return fail(res.status, { error: 'Delete failed' });
		return { success: true };
	},

	deleteSeries: async ({ request, cookies }) => {
		const token = cookies.get('admin_token') ?? '';
		const data = await request.formData();
		const id = data.get('id') as string;
		if (!id) return fail(400, { error: 'ID required' });

		const res = await fetch(`${VOD_SERVICE}/admin/vod/series/${id}`, {
			method: 'DELETE',
			headers: { Authorization: `Bearer ${token}` }
		});
		if (!res.ok) return fail(res.status, { error: 'Delete failed' });
		return { success: true };
	},

	toggleActive: async ({ request, cookies }) => {
		const token = cookies.get('admin_token') ?? '';
		const data = await request.formData();
		const id = data.get('id') as string;
		const vodType = data.get('type') as string;
		const isActive = data.get('is_active') === 'true';
		if (!id) return fail(400, { error: 'ID required' });

		const endpoint =
			vodType === 'series'
				? `${VOD_SERVICE}/admin/vod/series/${id}`
				: `${VOD_SERVICE}/admin/vod/movies/${id}`;

		const res = await fetch(endpoint, {
			method: 'PUT',
			headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
			body: JSON.stringify({ is_active: !isActive })
		});
		if (!res.ok) return fail(res.status, { error: 'Update failed' });
		return { success: true };
	}
};
