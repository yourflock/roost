import { fail, redirect } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { Actions, PageServerLoad } from './$types';
import { AdminApiClient } from '$lib/api';
import { setAdminSessionCookie } from '$lib/server/auth';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	// Already logged in — redirect
	if (event.locals.admin) {
		redirect(302, '/dashboard');
	}
	return {};
};

export const actions: Actions = {
	default: async (event) => {
		const data = await event.request.formData();
		const email = data.get('email') as string;
		const password = data.get('password') as string;

		if (!email || !password) {
			return fail(400, { error: 'Email and password required.', email });
		}

		const client = new AdminApiClient(API_URL);
		try {
			const result = await client.login(email, password);
			setAdminSessionCookie(event, result.token);
		} catch (err: unknown) {
			const apiErr = err as { message?: string; status?: number };
			if (apiErr.status === 403) {
				return fail(403, { error: 'Access denied. Admin access required.', email });
			}
			return fail(401, { error: 'Invalid email or password.', email });
		}

		redirect(302, '/dashboard');
	}
};
