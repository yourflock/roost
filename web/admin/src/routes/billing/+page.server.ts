import { redirect, fail } from '@sveltejs/kit';
import { env } from '$env/dynamic/private';
import type { Actions, PageServerLoad } from './$types';
import { AdminApiClient, type PromoCode } from '$lib/api';

const API_URL = env.ROOST_API_URL ?? 'http://localhost:4000';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) redirect(302, '/login');

	const client = new AdminApiClient(API_URL, event.locals.sessionToken);
	let stats = null;
	let promoCodes: PromoCode[] = [];

	try {
		[stats, promoCodes] = await Promise.all([client.getDashboardStats(), client.getPromoCodes()]);
	} catch {
		// Return whatever succeeded
	}

	return { stats, promoCodes };
};

export const actions: Actions = {
	createPromo: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();

		const code = (data.get('code') as string)?.trim().toUpperCase();
		const discount_type = data.get('discount_type') as 'percent' | 'fixed';
		const discount_value = parseFloat(data.get('discount_value') as string);
		const max_uses = parseInt(data.get('max_uses') as string) || null;
		const expires_at = (data.get('expires_at') as string) || null;
		const is_active = data.get('is_active') === 'on';

		if (!code || !discount_type || isNaN(discount_value)) {
			return fail(400, { promoError: 'Code, type, and discount value are required.' });
		}

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.createPromoCode({
				code,
				discount_type,
				discount_value,
				max_uses,
				expires_at,
				is_active
			});
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { promoError: e.message ?? 'Failed to create promo code.' });
		}

		return { promoSuccess: true };
	},

	deactivatePromo: async (event) => {
		if (!event.locals.admin) redirect(302, '/login');
		const data = await event.request.formData();
		const id = data.get('id') as string;

		if (!id) return fail(400, { promoError: 'Promo code ID required.' });

		const client = new AdminApiClient(API_URL, event.locals.sessionToken);
		try {
			await client.deactivatePromoCode(id);
		} catch (err: unknown) {
			const e = err as { message?: string };
			return fail(500, { promoError: e.message ?? 'Failed to deactivate promo code.' });
		}

		return { promoSuccess: true };
	}
};
