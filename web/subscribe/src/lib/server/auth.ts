import { env } from '$env/dynamic/private';
import type { RequestEvent } from '@sveltejs/kit';
import type { Subscriber } from '$lib/api';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const SESSION_COOKIE = 'roost_session';

/** Validate session token with Roost auth service and return subscriber, or null if invalid */
export async function validateSession(
	event: RequestEvent
): Promise<{ subscriber: Subscriber; token: string } | null> {
	const token = event.cookies.get(SESSION_COOKIE);
	if (!token) return null;

	try {
		const res = await fetch(`${API_URL}/auth/me`, {
			headers: { Authorization: `Bearer ${token}` }
		});
		if (!res.ok) return null;
		const subscriber: Subscriber = await res.json();
		return { subscriber, token };
	} catch {
		return null;
	}
}

/** Set session cookie (httpOnly, secure in prod) */
export function setSessionCookie(event: RequestEvent, token: string) {
	event.cookies.set(SESSION_COOKIE, token, {
		path: '/',
		httpOnly: true,
		secure: process.env.NODE_ENV === 'production',
		sameSite: 'lax',
		maxAge: 60 * 60 * 24 * 7 // 7 days
	});
}

/** Clear session cookie */
export function clearSessionCookie(event: RequestEvent) {
	event.cookies.delete(SESSION_COOKIE, { path: '/' });
}
