import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { SESSION_COOKIE } from '$lib/server/auth';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const POST: RequestHandler = async (event) => {
	const token = event.cookies.get(SESSION_COOKIE);
	if (!token) throw error(401, 'Unauthorized');

	const res = await fetch(`${API_URL}/billing/portal`, {
		method: 'POST',
		headers: { Authorization: `Bearer ${token}` }
	});

	if (!res.ok) {
		const body = await res.json().catch(() => ({ message: 'Failed to open billing portal.' }));
		throw error(res.status, body.message);
	}

	const data = await res.json();
	return json(data);
};
