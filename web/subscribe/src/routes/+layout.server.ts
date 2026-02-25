import type { LayoutServerLoad } from './$types';
import { validateSession } from '$lib/server/auth';

export const load: LayoutServerLoad = async (event) => {
	const session = await validateSession(event);
	return {
		subscriber: session?.subscriber ?? null,
		sessionToken: session?.token ?? null
	};
};
