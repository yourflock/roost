import { redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { clearSessionCookie, SESSION_COOKIE } from '$lib/server/auth';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async () => {
	throw redirect(303, '/');
};

export const actions: Actions = {
	default: async (event) => {
		const token = event.cookies.get(SESSION_COOKIE);
		if (token) {
			// Tell the backend to invalidate the session
			try {
				await fetch(`${API_URL}/auth/logout`, {
					method: 'POST',
					headers: { Authorization: `Bearer ${token}` }
				});
			} catch {
				// Ignore backend errors — clear cookie anyway
			}
		}
		clearSessionCookie(event);
		throw redirect(303, '/');
	}
};
