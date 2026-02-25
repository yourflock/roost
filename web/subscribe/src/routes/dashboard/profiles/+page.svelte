<!-- +page.svelte — Profile management page for the subscriber portal.
     P12-T04: Subscriber Portal — Profile Management UI
     Shows: profile grid, plan limit indicator, add/edit/delete modals. -->
<script lang="ts">
	import { enhance } from '$app/forms';
	import { page } from '$app/stores';
	import type { PageData, ActionData } from './$types';
	import type { Profile } from './+page.server';

	export let data: PageData;
	export let form: ActionData;

	// Modal state
	let showCreateModal = false;
	let showEditModal = false;
	let showDeleteModal = false;
	let selectedProfile: Profile | null = null;
	let deleteConfirmName = '';

	// Form state
	let createName = '';
	let createAvatarPreset = 'owl-1';
	let createAgeRating = '';
	let createIsKids = false;
	let createPIN = '';

	// Edit form state (populated when edit modal opens)
	let editName = '';
	let editAvatarPreset = '';
	let editAgeRating = '';
	let editIsKids = false;
	let editClearPIN = false;
	let editNewPIN = '';
	let editScheduleStart = '';
	let editScheduleEnd = '';
	let editTimezone = 'America/New_York';
	let editClearSchedule = false;

	let loading = false;
	let error = '';

	// Avatar presets
	const avatarPresets = Array.from({ length: 12 }, (_, i) => `owl-${i + 1}`);
	const ageRatingOptions = [
		{ value: '', label: 'No restriction' },
		{ value: 'TV-G', label: 'TV-G (General audiences)' },
		{ value: 'TV-PG', label: 'TV-PG (Parental guidance)' },
		{ value: 'TV-14', label: 'TV-14 (Parents strongly cautioned)' },
		{ value: 'TV-MA', label: 'TV-MA (Mature audiences only)' }
	];

	function openEdit(profile: Profile) {
		selectedProfile = profile;
		editName = profile.name;
		editAvatarPreset = profile.avatar_preset ?? '';
		editAgeRating = profile.age_rating_limit ?? '';
		editIsKids = profile.is_kids_profile;
		editClearPIN = false;
		editNewPIN = '';
		editScheduleStart = profile.viewing_schedule?.allowed_hours.start ?? '';
		editScheduleEnd = profile.viewing_schedule?.allowed_hours.end ?? '';
		editTimezone = profile.viewing_schedule?.timezone ?? 'America/New_York';
		editClearSchedule = false;
		showEditModal = true;
	}

	function openDelete(profile: Profile) {
		selectedProfile = profile;
		deleteConfirmName = '';
		showDeleteModal = true;
	}

	function closeModals() {
		showCreateModal = false;
		showEditModal = false;
		showDeleteModal = false;
		selectedProfile = null;
		error = '';
	}

	function avatarUrl(profile: Profile): string {
		if (profile.avatar_url) return profile.avatar_url;
		if (profile.avatar_preset) return `https://media.yourflock.org/avatars/presets/${profile.avatar_preset}.png`;
		return '/images/avatar-default.png';
	}

	$: successMsg = $page.url.searchParams.get('created')
		? 'Profile created.'
		: $page.url.searchParams.get('updated')
		? 'Profile updated.'
		: $page.url.searchParams.get('deleted')
		? 'Profile deleted.'
		: '';

	$: canAddProfile = data.limits.current < data.limits.max;
</script>

<svelte:head>
	<title>Profiles — Roost</title>
</svelte:head>

<div class="max-w-4xl mx-auto px-4 py-10">
	<!-- Header -->
	<div class="flex items-start justify-between mb-8">
		<div>
			<h1 class="text-2xl font-bold text-white">Profiles</h1>
			<p class="text-slate-400 text-sm mt-1">
				Manage who watches Roost. Each profile has its own watch history and parental controls.
			</p>
		</div>
		<a href="/dashboard" class="text-sm text-slate-400 hover:text-white">← Dashboard</a>
	</div>

	<!-- Success banner -->
	{#if successMsg}
		<div class="bg-green-900/30 border border-green-700/50 text-green-400 rounded-xl px-4 py-3 mb-6 text-sm">
			{successMsg}
		</div>
	{/if}

	<!-- Form action error -->
	{#if form?.error}
		<div class="bg-red-900/30 border border-red-700/50 text-red-400 rounded-xl px-4 py-3 mb-6 text-sm">
			{form.error}
		</div>
	{/if}

	<!-- Plan limit indicator -->
	<div class="card mb-6 flex items-center justify-between">
		<div>
			<span class="text-slate-300 text-sm font-medium">
				{data.limits.current} of {data.limits.max} profiles used
			</span>
			<span class="ml-2 text-xs text-slate-500 capitalize">({data.limits.plan} plan)</span>
		</div>
		{#if !canAddProfile}
			<span class="text-xs text-amber-400">Profile limit reached — upgrade to add more</span>
		{/if}
	</div>

	<!-- Profile grid -->
	<div class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 gap-4 mb-8">
		{#each data.profiles as profile (profile.id)}
			<div class="card text-center relative group {profile.is_active ? '' : 'opacity-50'}">
				<!-- Avatar -->
				<div class="w-16 h-16 mx-auto mb-3 relative">
					<img
						src={avatarUrl(profile)}
						alt="{profile.name} avatar"
						class="w-16 h-16 rounded-full object-cover bg-slate-700"
						on:error={(e) => { (e.target as HTMLImageElement).src='/images/avatar-default.png'; }}
					/>
					<!-- Badges -->
					{#if profile.is_primary}
						<span
							class="absolute -top-1 -right-1 bg-yellow-400 text-yellow-900 text-[10px] font-bold px-1.5 py-0.5 rounded-full"
							title="Primary profile"
						>
							★
						</span>
					{/if}
					{#if profile.is_kids_profile}
						<span
							class="absolute -bottom-1 -right-1 bg-blue-500 text-white text-[10px] font-bold px-1.5 py-0.5 rounded-full"
							title="Kids profile"
						>
							Kids
						</span>
					{/if}
				</div>

				<!-- Name -->
				<p class="text-white font-medium text-sm truncate px-1">{profile.name}</p>

				<!-- Rating badge -->
				{#if profile.age_rating_limit}
					<p class="text-xs text-slate-500 mt-1">Limit: {profile.age_rating_limit}</p>
				{/if}

				<!-- Actions -->
				<div class="mt-3 flex gap-2 justify-center opacity-0 group-hover:opacity-100 transition-opacity">
					<button
						on:click={() => openEdit(profile)}
						class="text-xs text-roost-400 hover:text-roost-300 font-medium"
					>
						Edit
					</button>
					{#if !profile.is_primary}
						<span class="text-slate-600">·</span>
						<button
							on:click={() => openDelete(profile)}
							class="text-xs text-red-400 hover:text-red-300 font-medium"
						>
							Delete
						</button>
					{/if}
				</div>
			</div>
		{/each}

		<!-- Add Profile card -->
		{#if canAddProfile}
			<button
				on:click={() => (showCreateModal = true)}
				class="card text-center flex flex-col items-center justify-center gap-3 border-dashed border-slate-600 hover:border-roost-500 hover:bg-slate-800/60 transition-all min-h-[140px]"
			>
				<div class="w-12 h-12 rounded-full bg-slate-700 flex items-center justify-center text-2xl text-slate-400">
					+
				</div>
				<span class="text-sm text-slate-400">Add Profile</span>
			</button>
		{/if}
	</div>

	<!-- Tips -->
	<div class="text-xs text-slate-600 space-y-1">
		<p>• Each profile has its own watch history and continue watching list.</p>
		<p>• Kids profiles show only age-appropriate content (TV-Y and TV-G).</p>
		<p>• Set a PIN to restrict which profiles others can switch to.</p>
		<p>• Viewing schedules let you restrict when a profile can watch (e.g., school nights).</p>
	</div>
</div>

<!-- ── Create Profile Modal ─────────────────────────────────────────────── -->
{#if showCreateModal}
	<div
		class="fixed inset-0 bg-black/70 z-50 flex items-center justify-center p-4"
		on:click|self={closeModals}
		on:keydown={(e) => e.key === 'Escape' && closeModals()}
		role="dialog"
		aria-modal="true"
		aria-label="Create profile"
		tabindex="-1"
	>
		<div class="card w-full max-w-md">
			<h2 class="text-lg font-semibold text-white mb-5">Add Profile</h2>

			<form
				method="POST"
				action="?/create"
				use:enhance={() => {
					loading = true;
					return async ({ update }) => {
						loading = false;
						await update();
					};
				}}
				class="space-y-4"
			>
				<!-- Name -->
				<div>
					<label for="create-name" class="block text-sm font-medium text-slate-300 mb-1">Name</label>
					<input
						id="create-name"
						name="name"
						type="text"
						bind:value={createName}
						class="input w-full"
						placeholder="e.g. Kids Room"
						maxlength="100"
						required
					/>
				</div>

				<!-- Avatar preset -->
				<div>
					<p class="text-sm font-medium text-slate-300 mb-2">Avatar</p>
					<div class="grid grid-cols-6 gap-2">
						{#each avatarPresets as preset}
							<label class="cursor-pointer">
								<input
									type="radio"
									name="avatar_preset"
									value={preset}
									bind:group={createAvatarPreset}
									class="sr-only"
								/>
								<img
									src="https://media.yourflock.org/avatars/presets/{preset}.png"
									alt={preset}
									class="w-9 h-9 rounded-full object-cover border-2 transition-all {createAvatarPreset === preset ? 'border-roost-500 scale-110' : 'border-transparent hover:border-slate-500'}"
									on:error={(e) => { (e.target as HTMLImageElement).src='/images/avatar-default.png'; }}
								/>
							</label>
						{/each}
					</div>
				</div>

				<!-- Kids toggle -->
				<div class="flex items-center gap-3">
					<input
						id="create-kids"
						name="is_kids_profile"
						type="checkbox"
						bind:checked={createIsKids}
						class="w-4 h-4 rounded border-slate-600 bg-slate-700 text-roost-500"
					/>
					<label for="create-kids" class="text-sm text-slate-300">
						Kids profile
						<span class="text-slate-500 text-xs">(simplified interface, only TV-Y and TV-G content)</span>
					</label>
				</div>

				<!-- Age rating limit (only shown if not kids) -->
				{#if !createIsKids}
					<div>
						<label for="create-rating" class="block text-sm font-medium text-slate-300 mb-1">Age rating limit</label>
						<select
							id="create-rating"
							name="age_rating_limit"
							bind:value={createAgeRating}
							class="input w-full"
						>
							{#each ageRatingOptions as opt}
								<option value={opt.value}>{opt.label}</option>
							{/each}
						</select>
					</div>
				{/if}

				<!-- PIN (optional) -->
				<div>
					<label for="create-pin" class="block text-sm font-medium text-slate-300 mb-1">
						PIN <span class="text-slate-500 text-xs">(optional — 4 digits)</span>
					</label>
					<input
						id="create-pin"
						name="pin"
						type="password"
						bind:value={createPIN}
						class="input w-full"
						placeholder="Leave blank for no PIN"
						maxlength="4"
						pattern="[0-9]{4}"
					/>
				</div>

				<div class="flex gap-3 pt-2">
					<button type="submit" disabled={loading} class="btn-primary flex-1 py-2.5">
						{loading ? 'Creating…' : 'Create Profile'}
					</button>
					<button type="button" on:click={closeModals} class="btn-secondary flex-1 py-2.5">
						Cancel
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- ── Edit Profile Modal ──────────────────────────────────────────────── -->
{#if showEditModal && selectedProfile}
	<div
		class="fixed inset-0 bg-black/70 z-50 flex items-center justify-center p-4 overflow-y-auto"
		on:click|self={closeModals}
		on:keydown={(e) => e.key === 'Escape' && closeModals()}
		role="dialog"
		aria-modal="true"
		aria-label="Edit profile"
		tabindex="-1"
	>
		<div class="card w-full max-w-md my-auto">
			<h2 class="text-lg font-semibold text-white mb-5">Edit Profile — {selectedProfile.name}</h2>

			<form
				method="POST"
				action="?/update"
				use:enhance={() => {
					loading = true;
					return async ({ update }) => {
						loading = false;
						await update();
					};
				}}
				class="space-y-4"
			>
				<input type="hidden" name="profile_id" value={selectedProfile.id} />

				<!-- Name -->
				<div>
					<label for="edit-name" class="block text-sm font-medium text-slate-300 mb-1">Name</label>
					<input
						id="edit-name"
						name="name"
						type="text"
						bind:value={editName}
						class="input w-full"
						maxlength="100"
						required
					/>
				</div>

				<!-- Avatar preset -->
				<div>
					<p class="text-sm font-medium text-slate-300 mb-2">Avatar</p>
					<div class="grid grid-cols-6 gap-2">
						{#each avatarPresets as preset}
							<label class="cursor-pointer">
								<input
									type="radio"
									name="avatar_preset"
									value={preset}
									bind:group={editAvatarPreset}
									class="sr-only"
								/>
								<img
									src="https://media.yourflock.org/avatars/presets/{preset}.png"
									alt={preset}
									class="w-9 h-9 rounded-full object-cover border-2 transition-all {editAvatarPreset === preset ? 'border-roost-500 scale-110' : 'border-transparent hover:border-slate-500'}"
									on:error={(e) => { (e.target as HTMLImageElement).src='/images/avatar-default.png'; }}
								/>
							</label>
						{/each}
					</div>
				</div>

				<!-- Kids toggle -->
				{#if !selectedProfile.is_primary}
					<div class="flex items-center gap-3">
						<input
							id="edit-kids"
							name="is_kids_profile"
							type="checkbox"
							bind:checked={editIsKids}
							class="w-4 h-4 rounded border-slate-600 bg-slate-700 text-roost-500"
						/>
						<label for="edit-kids" class="text-sm text-slate-300">
							Kids profile
							<span class="text-slate-500 text-xs">(TV-Y and TV-G content only)</span>
						</label>
					</div>
				{/if}

				<!-- Age rating limit (only shown if not kids) -->
				{#if !editIsKids}
					<div>
						<label for="edit-rating" class="block text-sm font-medium text-slate-300 mb-1">Age rating limit</label>
						<select id="edit-rating" name="age_rating_limit" bind:value={editAgeRating} class="input w-full">
							{#each ageRatingOptions as opt}
								<option value={opt.value}>{opt.label}</option>
							{/each}
						</select>
					</div>
				{/if}

				<!-- PIN management -->
				<div class="space-y-2">
					<p class="text-sm font-medium text-slate-300">PIN Protection</p>
					{#if selectedProfile.has_pin}
						<div class="flex items-center gap-3">
							<input
								id="edit-clear-pin"
								name="clear_pin"
								type="checkbox"
								value="true"
								bind:checked={editClearPIN}
								class="w-4 h-4 rounded border-slate-600"
							/>
							<label for="edit-clear-pin" class="text-sm text-slate-400">Remove existing PIN</label>
						</div>
					{/if}
					{#if !editClearPIN}
						<input
							name="pin"
							type="password"
							bind:value={editNewPIN}
							class="input w-full"
							placeholder={selectedProfile.has_pin ? 'New PIN (leave blank to keep)' : 'Set a 4-digit PIN (optional)'}
							maxlength="4"
							pattern="[0-9]{4}"
						/>
					{/if}
				</div>

				<!-- Viewing schedule -->
				<div class="space-y-2">
					<p class="text-sm font-medium text-slate-300">Viewing Hours</p>
					{#if selectedProfile.viewing_schedule && !editClearSchedule}
						<div class="flex items-center gap-3">
							<input
								id="edit-clear-sched"
								name="clear_schedule"
								type="checkbox"
								value="true"
								bind:checked={editClearSchedule}
								class="w-4 h-4 rounded border-slate-600"
							/>
							<label for="edit-clear-sched" class="text-sm text-slate-400">Remove viewing schedule</label>
						</div>
					{/if}
					{#if !editClearSchedule}
						<div class="flex gap-2 items-center">
							<input
								name="schedule_start"
								type="time"
								bind:value={editScheduleStart}
								class="input flex-1"
								placeholder="08:00"
							/>
							<span class="text-slate-500 text-sm">to</span>
							<input
								name="schedule_end"
								type="time"
								bind:value={editScheduleEnd}
								class="input flex-1"
								placeholder="21:00"
							/>
						</div>
						<p class="text-xs text-slate-500">Leave blank for no time restriction.</p>
						<div>
							<label for="edit-tz" class="block text-xs text-slate-500 mb-1">Timezone</label>
							<input
								id="edit-tz"
								name="timezone"
								type="text"
								bind:value={editTimezone}
								class="input w-full text-sm"
								placeholder="America/New_York"
							/>
						</div>
					{/if}
				</div>

				<div class="flex gap-3 pt-2">
					<button type="submit" disabled={loading} class="btn-primary flex-1 py-2.5">
						{loading ? 'Saving…' : 'Save Changes'}
					</button>
					<button type="button" on:click={closeModals} class="btn-secondary flex-1 py-2.5">
						Cancel
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- ── Delete Profile Modal ───────────────────────────────────────────── -->
{#if showDeleteModal && selectedProfile}
	<div
		class="fixed inset-0 bg-black/70 z-50 flex items-center justify-center p-4"
		on:click|self={closeModals}
		on:keydown={(e) => e.key === 'Escape' && closeModals()}
		role="dialog"
		aria-modal="true"
		aria-label="Delete profile"
		tabindex="-1"
	>
		<div class="card w-full max-w-sm">
			<h2 class="text-lg font-semibold text-white mb-3">Delete Profile</h2>
			<p class="text-slate-300 text-sm mb-4">
				This will permanently delete <strong class="text-white">{selectedProfile.name}</strong>'s
				watch history and preferences. This cannot be undone.
			</p>
			<p class="text-slate-400 text-sm mb-3">
				Type <strong class="text-white">{selectedProfile.name}</strong> to confirm:
			</p>
			<input
				type="text"
				bind:value={deleteConfirmName}
				class="input w-full mb-4"
				placeholder={selectedProfile.name}
			/>

			<form
				method="POST"
				action="?/delete"
				use:enhance={() => {
					loading = true;
					return async ({ update }) => {
						loading = false;
						await update();
					};
				}}
			>
				<input type="hidden" name="profile_id" value={selectedProfile.id} />
				<div class="flex gap-3">
					<button
						type="submit"
						disabled={deleteConfirmName !== selectedProfile.name || loading}
						class="btn-primary flex-1 py-2.5 bg-red-600 hover:bg-red-500 disabled:opacity-40"
					>
						{loading ? 'Deleting…' : 'Delete Profile'}
					</button>
					<button type="button" on:click={closeModals} class="btn-secondary flex-1 py-2.5">
						Cancel
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
