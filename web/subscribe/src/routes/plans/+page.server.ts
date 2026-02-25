import type { PageServerLoad } from './$types';
import type { Plan } from '$lib/api';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

// Default plans if API unavailable
const DEFAULT_PLANS: Plan[] = [
	{
		id: 'basic',
		name: 'Basic',
		description: 'Live TV and EPG for individuals.',
		price_monthly: 599,
		price_annual: 5990,
		max_streams: 2,
		features: ['200+ live channels', 'Full EPG (14 days)', '2 concurrent streams', 'Standard quality (1080p)', 'Works with Owl, TiviMate, VLC']
	},
	{
		id: 'premium',
		name: 'Premium',
		description: 'The full Roost experience.',
		price_monthly: 999,
		price_annual: 9990,
		max_streams: 4,
		features: ['200+ live channels', 'Full EPG (14 days)', '4 concurrent streams', 'HD + 4K quality', 'Sports intelligence + commercial skip', 'VOD library access', 'Priority support'],
		is_popular: true
	},
	{
		id: 'family',
		name: 'Family',
		description: 'Whole family, one subscription.',
		price_monthly: 1499,
		price_annual: 14990,
		max_streams: 6,
		features: ['200+ live channels', 'Full EPG (14 days)', '6 concurrent streams', 'HD + 4K quality', 'Sports intelligence + commercial skip', 'VOD library access', 'Parental controls', 'Priority support']
	}
];

export const load: PageServerLoad = async () => {
	let plans: Plan[] = DEFAULT_PLANS;

	try {
		const res = await fetch(`${API_URL}/billing/plans`);
		if (res.ok) {
			plans = await res.json();
		}
	} catch {
		// Use default plans when backend is unreachable
	}

	return { plans };
};
