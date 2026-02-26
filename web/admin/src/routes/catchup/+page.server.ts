// +page.server.ts — Catchup/DVR admin dashboard
// Shows per-channel recording status, storage usage, and controls.

import { fail } from '@sveltejs/kit';
import type { PageServerLoad, Actions } from './$types';

const CATCHUP_SERVICE = process.env.CATCHUP_SERVICE_URL ?? 'http://localhost:8098';

export const load: PageServerLoad = async () => {
	const res = await fetch(`${CATCHUP_SERVICE}/catchup/status`).catch(() => null);
	const channels = res?.ok ? ((await res.json()).channels ?? []) : [];
	return { channels };
};

export const actions: Actions = {
	updateSettings: async ({ request }) => {
		const data = await request.formData();
		const channelSlug = data.get('channel_slug') as string;
		const enabled = data.get('enabled') === 'true';
		const retentionDays = parseInt(data.get('retention_days') as string, 10) || 7;

		if (!channelSlug) return fail(400, { error: 'channel_slug required' });

		const res = await fetch(`${CATCHUP_SERVICE}/catchup/settings/${channelSlug}`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ enabled, retention_days: retentionDays })
		});
		if (!res.ok) return fail(res.status, { error: 'Settings update failed' });
		return { success: true };
	},

	triggerCleanup: async () => {
		const res = await fetch(`${CATCHUP_SERVICE}/internal/catchup/cleanup`, {
			method: 'POST'
		});
		if (!res.ok) return fail(res.status, { error: 'Cleanup trigger failed' });
		return { success: true };
	}
};
