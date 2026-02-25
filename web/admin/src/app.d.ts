// See https://svelte.dev/docs/kit/types#app.d.ts
// for information about these interfaces
declare global {
	namespace App {
		interface Locals {
			admin: {
				id: string;
				email: string;
				name: string;
				role: 'superowner' | 'staff';
			} | null;
			sessionToken: string | null;
		}
		// interface Error {}
		// interface PageData {}
		// interface PageState {}
		// interface Platform {}
	}
}

export {};
