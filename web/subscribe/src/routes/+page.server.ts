import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async () => {
	// Landing page — no auth required, no data needed from server
	return {};
};
