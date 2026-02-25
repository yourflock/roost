import { writable } from 'svelte/store';
import type { Subscription, ApiToken, Invoice } from '$lib/api';

export interface SubscriberState {
	subscription: Subscription | null;
	apiToken: ApiToken | null;
	invoices: Invoice[];
	loading: boolean;
	error: string | null;
}

export const subscriberStore = writable<SubscriberState>({
	subscription: null,
	apiToken: null,
	invoices: [],
	loading: false,
	error: null
});
