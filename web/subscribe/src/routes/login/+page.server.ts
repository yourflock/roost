import { fail, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { setSessionCookie } from '$lib/server/auth';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async ({ parent }) => {
	const { subscriber } = await parent();
	if (subscriber) throw redirect(303, '/dashboard');
	return {};
};

export const actions: Actions = {
	default: async (event) => {
		const data = await event.request.formData();
		const email = data.get('email')?.toString().trim() ?? '';
		const password = data.get('password')?.toString() ?? '';

		if (!email || !password) {
			return fail(400, { error: 'Email and password are required.' });
		}

		try {
			const res = await fetch(`${API_URL}/auth/login`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ email, password })
			});

			if (!res.ok) {
				const body = await res.json().catch(() => ({ message: 'Login failed.' }));
				return fail(res.status, { error: body.message ?? 'Invalid email or password.' });
			}

			const { session_token } = await res.json();
			setSessionCookie(event, session_token);
		} catch {
			return fail(500, { error: 'Service unavailable. Please try again.' });
		}

		throw redirect(303, '/dashboard');
	}
};
