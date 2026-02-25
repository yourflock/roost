import { writable } from 'svelte/store';
import type { Subscriber } from '$lib/api';

export interface AuthState {
	subscriber: Subscriber | null;
	sessionToken: string | null;
	loading: boolean;
}

const initialState: AuthState = {
	subscriber: null,
	sessionToken: null,
	loading: false
};

export const authStore = writable<AuthState>(initialState);

export function setAuth(subscriber: Subscriber, sessionToken: string) {
	authStore.set({ subscriber, sessionToken, loading: false });
}

export function clearAuth() {
	authStore.set({ subscriber: null, sessionToken: null, loading: false });
}
