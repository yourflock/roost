import { redirect } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';
import type { Plan } from '$lib/api';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

const DEFAULT_PLANS: Plan[] = [
	{
		id: 'basic',
		name: 'Basic',
		description: 'Live TV and EPG for individuals.',
		price_monthly: 599,
		price_annual: 5990,
		max_streams: 2,
		features: ['200+ live channels', '2 concurrent streams', 'Standard quality']
	},
	{
		id: 'premium',
		name: 'Premium',
		description: 'The full Roost experience.',
		price_monthly: 999,
		price_annual: 9990,
		max_streams: 4,
		features: ['200+ live channels', '4 concurrent streams', 'HD + 4K', 'Sports + commercial skip', 'VOD library'],
		is_popular: true
	},
	{
		id: 'family',
		name: 'Family',
		description: 'Whole family, one subscription.',
		price_monthly: 1499,
		price_annual: 14990,
		max_streams: 6,
		features: ['200+ live channels', '6 concurrent streams', 'HD + 4K', 'Sports + commercial skip', 'VOD library', 'Parental controls']
	}
];

export const load: PageServerLoad = async ({ parent, url }) => {
	const { subscriber } = await parent();
	// If already subscribed, send to dashboard
	if (subscriber) throw redirect(303, '/dashboard');

	const selectedPlan = url.searchParams.get('plan') ?? 'premium';
	const selectedPeriod = url.searchParams.get('period') as 'monthly' | 'annual' ?? 'monthly';

	let plans: Plan[] = DEFAULT_PLANS;
	try {
		const res = await fetch(`${API_URL}/billing/plans`);
		if (res.ok) plans = await res.json();
	} catch {
		// use defaults
	}

	return { plans, selectedPlan, selectedPeriod };
};
