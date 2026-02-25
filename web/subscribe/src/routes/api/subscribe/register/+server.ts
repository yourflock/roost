import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { setSessionCookie } from '$lib/server/auth';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export const POST: RequestHandler = async (event) => {
	const body = await event.request.json();
	const { name, email, password, plan_id, billing_period } = body;

	if (!name || !email || !password || !plan_id || !billing_period) {
		throw error(400, 'Missing required fields.');
	}

	// Register the account
	const registerRes = await fetch(`${API_URL}/auth/register`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ name, email, password })
	});

	if (!registerRes.ok) {
		const body = await registerRes.json().catch(() => ({ message: 'Registration failed.' }));
		throw error(registerRes.status, body.message);
	}

	const { session_token } = await registerRes.json();
	setSessionCookie(event, session_token);

	// Create Stripe checkout session
	const checkoutRes = await fetch(`${API_URL}/billing/checkout`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${session_token}` },
		body: JSON.stringify({ plan_id, billing_period })
	});

	if (!checkoutRes.ok) {
		const body = await checkoutRes.json().catch(() => ({ message: 'Checkout failed.' }));
		throw error(checkoutRes.status, body.message);
	}

	const checkoutData = await checkoutRes.json();
	return json({ checkout_url: checkoutData.checkout_url });
};
