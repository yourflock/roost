// +page.server.ts — Regions page data loader (P14-T02)
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ fetch }) => {
	try {
		// Fetch regions with subscriber + channel counts
		const [regionsRes] = await Promise.all([fetch('/api/admin/regions')]);

		if (!regionsRes.ok) {
			return { regions: [] };
		}

		const regionsData = await regionsRes.json();
		return {
			regions: regionsData.regions ?? []
		};
	} catch {
		return { regions: [] };
	}
};
