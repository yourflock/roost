import { redirect, fail } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { SESSION_COOKIE, clearSessionCookie } from '$lib/server/auth';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async ({ parent }) => {
	const { subscriber } = await parent();
	if (!subscriber) throw redirect(303, '/login');
	return { subscriber };
};

export const actions: Actions = {
	changePassword: async (event) => {
		const token = event.cookies.get(SESSION_COOKIE) ?? '';
		const data = await event.request.formData();
		const currentPassword = data.get('current_password')?.toString() ?? '';
		const newPassword = data.get('new_password')?.toString() ?? '';
		const confirmPassword = data.get('confirm_password')?.toString() ?? '';

		if (!currentPassword || !newPassword || !confirmPassword) {
			return fail(400, { action: 'password', error: 'All fields are required.' });
		}
		if (newPassword !== confirmPassword) {
			return fail(400, { action: 'password', error: 'New passwords do not match.' });
		}
		if (newPassword.length < 8) {
			return fail(400, {
				action: 'password',
				error: 'New password must be at least 8 characters.'
			});
		}

		try {
			const res = await fetch(`${API_URL}/auth/change-password`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
				body: JSON.stringify({ current_password: currentPassword, new_password: newPassword })
			});
			if (!res.ok) {
				const body = await res.json().catch(() => ({}));
				return fail(res.status, {
					action: 'password',
					error: body.message ?? 'Failed to change password.'
				});
			}
		} catch {
			return fail(500, { action: 'password', error: 'Service unavailable.' });
		}

		return { action: 'password', success: 'Password updated successfully.' };
	},

	changeEmail: async (event) => {
		const token = event.cookies.get(SESSION_COOKIE) ?? '';
		const data = await event.request.formData();
		const email = data.get('email')?.toString().trim() ?? '';
		const password = data.get('password')?.toString() ?? '';

		if (!email || !password) {
			return fail(400, { action: 'email', error: 'Email and password are required.' });
		}

		try {
			const res = await fetch(`${API_URL}/auth/change-email`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
				body: JSON.stringify({ email, password })
			});
			if (!res.ok) {
				const body = await res.json().catch(() => ({}));
				return fail(res.status, {
					action: 'email',
					error: body.message ?? 'Failed to change email.'
				});
			}
		} catch {
			return fail(500, { action: 'email', error: 'Service unavailable.' });
		}

		return { action: 'email', success: 'Email updated successfully.' };
	},

	deleteAccount: async (event) => {
		const token = event.cookies.get(SESSION_COOKIE) ?? '';
		const data = await event.request.formData();
		const password = data.get('password')?.toString() ?? '';

		if (!password) {
			return fail(400, { action: 'delete', error: 'Password is required to delete your account.' });
		}

		try {
			const res = await fetch(`${API_URL}/auth/delete-account`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
				body: JSON.stringify({ password })
			});
			if (!res.ok) {
				const body = await res.json().catch(() => ({}));
				return fail(res.status, {
					action: 'delete',
					error: body.message ?? 'Failed to delete account.'
				});
			}
		} catch {
			return fail(500, { action: 'delete', error: 'Service unavailable.' });
		}

		clearSessionCookie(event);
		throw redirect(303, '/?deleted=1');
	}
};
