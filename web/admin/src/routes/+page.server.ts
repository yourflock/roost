import { redirect } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async (event) => {
	if (!event.locals.admin) {
		redirect(302, '/login');
	}
	redirect(302, '/dashboard');
};
