// +page.server.ts — Profile management page server load and form actions.
// P12-T04: Subscriber Portal — Profile Management UI
import { redirect, fail } from '@sveltejs/kit';
import type { PageServerLoad, Actions } from './$types';
import { SESSION_COOKIE } from '$lib/server/auth';
import { env } from '$env/dynamic/private';

const API_URL = env.API_URL ?? 'http://localhost:4000';

export interface Profile {
	id: string;
	name: string;
	avatar_url?: string;
	avatar_preset?: string;
	is_primary: boolean;
	age_rating_limit?: string;
	is_kids_profile: boolean;
	has_pin: boolean;
	is_active: boolean;
	blocked_categories: string[];
	viewing_schedule?: {
		allowed_hours: { start: string; end: string };
		timezone: string;
	};
	preferences: Record<string, unknown>;
	created_at: string;
}

export interface ProfileLimits {
	current: number;
	max: number;
	plan: string;
}

export const load: PageServerLoad = async (event) => {
	const { subscriber } = await event.parent();
	if (!subscriber) throw redirect(303, '/login');

	const token = event.cookies.get(SESSION_COOKIE) ?? '';
	const headers = { Authorization: `Bearer ${token}` };

	const [profilesRes, limitsRes] = await Promise.allSettled([
		fetch(`${API_URL}/profiles`, { headers }),
		fetch(`${API_URL}/profiles/limits`, { headers })
	]);

	let profiles: Profile[] = [];
	let limits: ProfileLimits = { current: 0, max: 2, plan: 'basic' };

	if (profilesRes.status === 'fulfilled' && profilesRes.value.ok) {
		const body = await profilesRes.value.json();
		profiles = body.profiles ?? [];
	}

	if (limitsRes.status === 'fulfilled' && limitsRes.value.ok) {
		limits = await limitsRes.value.json();
	}

	return { subscriber, profiles, limits };
};

export const actions: Actions = {
	// Create a new profile
	create: async (event) => {
		const token = event.cookies.get(SESSION_COOKIE) ?? '';
		const data = await event.request.formData();
		const name = data.get('name')?.toString().trim() ?? '';
		const avatarPreset = data.get('avatar_preset')?.toString() ?? '';
		const ageRatingLimit = data.get('age_rating_limit')?.toString() || null;
		const isKids = data.get('is_kids_profile') === 'on';
		const pin = data.get('pin')?.toString() ?? '';

		if (!name) return fail(400, { error: 'Profile name is required.' });

		const body: Record<string, unknown> = { name };
		if (avatarPreset) body.avatar_preset = avatarPreset;
		if (ageRatingLimit) body.age_rating_limit = ageRatingLimit;
		if (isKids) body.is_kids_profile = true;
		if (pin) body.pin = pin;

		const res = await fetch(`${API_URL}/profiles`, {
			method: 'POST',
			headers: {
				Authorization: `Bearer ${token}`,
				'Content-Type': 'application/json'
			},
			body: JSON.stringify(body)
		});

		if (!res.ok) {
			const err = await res.json();
			return fail(res.status, { error: err.message ?? 'Failed to create profile.' });
		}

		throw redirect(303, '/dashboard/profiles?created=1');
	},

	// Update an existing profile
	update: async (event) => {
		const token = event.cookies.get(SESSION_COOKIE) ?? '';
		const data = await event.request.formData();
		const profileId = data.get('profile_id')?.toString() ?? '';
		if (!profileId) return fail(400, { error: 'Profile ID is required.' });

		const body: Record<string, unknown> = {};
		const name = data.get('name')?.toString().trim();
		if (name) body.name = name;

		const avatarPreset = data.get('avatar_preset')?.toString();
		if (avatarPreset) body.avatar_preset = avatarPreset;

		const ageRatingLimit = data.get('age_rating_limit')?.toString();
		if (ageRatingLimit !== undefined) {
			body.age_rating_limit = ageRatingLimit || null;
		}

		const isKids = data.get('is_kids_profile');
		if (isKids !== null) body.is_kids_profile = isKids === 'on';

		const clearPin = data.get('clear_pin') === 'true';
		if (clearPin) {
			body.clear_pin = true;
		} else {
			const pin = data.get('pin')?.toString();
			if (pin) body.pin = pin;
		}

		const schedStart = data.get('schedule_start')?.toString();
		const schedEnd = data.get('schedule_end')?.toString();
		const timezone = data.get('timezone')?.toString() || 'UTC';
		const clearSchedule = data.get('clear_schedule') === 'true';

		if (clearSchedule) {
			body.clear_viewing_schedule = true;
		} else if (schedStart && schedEnd) {
			body.viewing_schedule = {
				allowed_hours: { start: schedStart, end: schedEnd },
				timezone
			};
		}

		const res = await fetch(`${API_URL}/profiles/${profileId}`, {
			method: 'PUT',
			headers: {
				Authorization: `Bearer ${token}`,
				'Content-Type': 'application/json'
			},
			body: JSON.stringify(body)
		});

		if (!res.ok) {
			const err = await res.json();
			return fail(res.status, { error: err.message ?? 'Failed to update profile.' });
		}

		throw redirect(303, '/dashboard/profiles?updated=1');
	},

	// Delete a profile
	delete: async (event) => {
		const token = event.cookies.get(SESSION_COOKIE) ?? '';
		const data = await event.request.formData();
		const profileId = data.get('profile_id')?.toString() ?? '';
		if (!profileId) return fail(400, { error: 'Profile ID is required.' });

		const res = await fetch(`${API_URL}/profiles/${profileId}`, {
			method: 'DELETE',
			headers: { Authorization: `Bearer ${token}` }
		});

		if (!res.ok) {
			const err = await res.json();
			return fail(res.status, { error: err.message ?? 'Failed to delete profile.' });
		}

		throw redirect(303, '/dashboard/profiles?deleted=1');
	}
};
