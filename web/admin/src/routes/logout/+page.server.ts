import { redirect } from '@sveltejs/kit';
import type { Actions } from './$types';
import { clearAdminSessionCookie } from '$lib/server/auth';

export const actions: Actions = {
	default: async (event) => {
		clearAdminSessionCookie(event);
		redirect(302, '/login');
	}
};

// GET fallback
export function load() {
	redirect(302, '/login');
}
