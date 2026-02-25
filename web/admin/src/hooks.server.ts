import type { Handle } from '@sveltejs/kit';
import { validateAdminSession } from '$lib/server/auth';

export const handle: Handle = async ({ event, resolve }) => {
	const session = await validateAdminSession(event);
	event.locals.admin = session?.admin ?? null;
	event.locals.sessionToken = session?.token ?? null;
	return resolve(event);
};
