import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { SESSION_COOKIE } from '$lib/server/auth';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const POST: RequestHandler = async (event) => {
	const token = event.cookies.get(SESSION_COOKIE);
	if (!token) throw error(401, 'Unauthorized');

	const res = await fetch(`${API_URL}/billing/api-token/regenerate`, {
		method: 'POST',
		headers: { Authorization: `Bearer ${token}` }
	});

	if (!res.ok) {
		const body = await res.json().catch(() => ({ message: 'Failed to regenerate token.' }));
		throw error(res.status, body.message);
	}

	const apiToken = await res.json();
	return json(apiToken);
};
