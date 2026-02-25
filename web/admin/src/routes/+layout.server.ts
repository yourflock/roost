import type { LayoutServerLoad } from './$types';

export const load: LayoutServerLoad = async (event) => {
	return {
		admin: event.locals.admin,
		sessionToken: event.locals.sessionToken
	};
};
