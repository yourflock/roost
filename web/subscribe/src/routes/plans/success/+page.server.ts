import { redirect } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ parent }) => {
	const { subscriber } = await parent();
	if (!subscriber) throw redirect(303, '/login');
	return {};
};
