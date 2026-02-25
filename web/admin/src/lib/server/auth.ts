import { env } from '$env/dynamic/private';
import type { RequestEvent } from '@sveltejs/kit';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const ADMIN_SESSION_COOKIE = 'roost_admin_session';

export interface AdminUser {
	id: string;
	email: string;
	name: string;
	role: 'superowner' | 'staff';
	is_superowner: boolean;
}

/** Validate admin session token — must have role superowner or staff */
export async function validateAdminSession(
	event: RequestEvent
): Promise<{ admin: AdminUser; token: string } | null> {
	const token = event.cookies.get(ADMIN_SESSION_COOKIE);
	if (!token) return null;

	try {
		const res = await fetch(`${API_URL}/auth/admin/me`, {
			headers: { Authorization: `Bearer ${token}` }
		});
		if (!res.ok) return null;
		const admin: AdminUser = await res.json();

		// Must have admin role
		if (!admin.is_superowner && admin.role !== 'staff') return null;

		return { admin, token };
	} catch {
		return null;
	}
}

/** Set admin session cookie (httpOnly, secure in prod) */
export function setAdminSessionCookie(event: RequestEvent, token: string) {
	event.cookies.set(ADMIN_SESSION_COOKIE, token, {
		path: '/',
		httpOnly: true,
		secure: process.env.NODE_ENV === 'production',
		sameSite: 'strict', // stricter than subscriber portal
		maxAge: 60 * 60 * 8 // 8-hour admin sessions
	});
}

/** Clear admin session cookie */
export function clearAdminSessionCookie(event: RequestEvent) {
	event.cookies.delete(ADMIN_SESSION_COOKIE, { path: '/' });
}
