<script lang="ts">
	import type { PageData, ActionData } from './$types';
	export let data: PageData;
	export let form: ActionData;

	let showDeleteConfirm = false;
</script>

<svelte:head>
	<title>Account — Roost</title>
</svelte:head>

<div class="max-w-2xl mx-auto px-4 py-10">
	<div class="mb-8">
		<h1 class="text-2xl font-bold text-white">Account Settings</h1>
		<p class="text-slate-400 text-sm mt-1">{data.subscriber.email}</p>
	</div>

	<!-- Change Email -->
	<div class="card mb-6">
		<h2 class="font-semibold text-white mb-4">Change Email</h2>

		{#if form?.action === 'email' && form?.error}
			<div
				class="bg-red-500/10 border border-red-500/30 rounded-lg px-3 py-2 text-red-400 text-sm mb-3"
			>
				{form.error}
			</div>
		{/if}
		{#if form?.action === 'email' && form?.success}
			<div
				class="bg-green-500/10 border border-green-500/30 rounded-lg px-3 py-2 text-green-400 text-sm mb-3"
			>
				{form.success}
			</div>
		{/if}

		<form method="POST" action="?/changeEmail" class="space-y-3">
			<div>
				<label for="email" class="label">New Email</label>
				<input
					id="email"
					name="email"
					type="email"
					required
					class="input"
					placeholder="newemail@example.com"
				/>
			</div>
			<div>
				<label for="email-password" class="label">Current Password</label>
				<input
					id="email-password"
					name="password"
					type="password"
					required
					class="input"
					placeholder="Confirm with your password"
				/>
			</div>
			<button type="submit" class="btn-secondary text-sm">Update Email</button>
		</form>
	</div>

	<!-- Change Password -->
	<div class="card mb-6">
		<h2 class="font-semibold text-white mb-4">Change Password</h2>

		{#if form?.action === 'password' && form?.error}
			<div
				class="bg-red-500/10 border border-red-500/30 rounded-lg px-3 py-2 text-red-400 text-sm mb-3"
			>
				{form.error}
			</div>
		{/if}
		{#if form?.action === 'password' && form?.success}
			<div
				class="bg-green-500/10 border border-green-500/30 rounded-lg px-3 py-2 text-green-400 text-sm mb-3"
			>
				{form.success}
			</div>
		{/if}

		<form method="POST" action="?/changePassword" class="space-y-3">
			<div>
				<label for="current-password" class="label">Current Password</label>
				<input
					id="current-password"
					name="current_password"
					type="password"
					required
					class="input"
					placeholder="Current password"
				/>
			</div>
			<div>
				<label for="new-password" class="label">New Password</label>
				<input
					id="new-password"
					name="new_password"
					type="password"
					required
					minlength="8"
					class="input"
					placeholder="At least 8 characters"
				/>
			</div>
			<div>
				<label for="confirm-password" class="label">Confirm New Password</label>
				<input
					id="confirm-password"
					name="confirm_password"
					type="password"
					required
					class="input"
					placeholder="Repeat new password"
				/>
			</div>
			<button type="submit" class="btn-secondary text-sm">Update Password</button>
		</form>
	</div>

	<!-- Flock Account Link -->
	<div class="card mb-6">
		<h2 class="font-semibold text-white mb-1">Flock Account</h2>
		<p class="text-slate-400 text-sm mb-4">
			Link your Flock family account to enable screen time controls, watch parties, and family
			activity sharing.
		</p>

		{#if form?.action === 'flock' && form?.error}
			<div
				class="bg-red-500/10 border border-red-500/30 rounded-lg px-3 py-2 text-red-400 text-sm mb-3"
			>
				{form.error}
			</div>
		{/if}
		{#if form?.action === 'flock' && form?.success}
			<div
				class="bg-green-500/10 border border-green-500/30 rounded-lg px-3 py-2 text-green-400 text-sm mb-3"
			>
				{form.success}
			</div>
		{/if}

		{#if data.subscriber.flock_user_id}
			<div class="flex items-center justify-between">
				<div>
					<p class="text-sm text-slate-300">Linked to Flock</p>
					<p class="text-xs text-slate-500 mt-0.5">User ID: {data.subscriber.flock_user_id}</p>
				</div>
				<form method="POST" action="?/unlinkFlock">
					<button type="submit" class="btn-secondary text-sm text-red-400 hover:text-red-300"
						>Unlink</button
					>
				</form>
			</div>
		{:else}
			<a href="/auth/flock/login" class="flex items-center gap-2 btn-secondary text-sm w-fit">
				<svg class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor">
					<path
						d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 14H9V8h2v8zm4 0h-2V8h2v8z"
					/>
				</svg>
				Link Flock Account
			</a>
		{/if}
	</div>

	<!-- Delete Account -->
	<div class="card border-red-900/50">
		<h2 class="font-semibold text-red-400 mb-2">Delete Account</h2>
		<p class="text-slate-400 text-sm mb-4">
			This permanently deletes your account and cancels any active subscription. This cannot be
			undone.
		</p>

		{#if form?.action === 'delete' && form?.error}
			<div
				class="bg-red-500/10 border border-red-500/30 rounded-lg px-3 py-2 text-red-400 text-sm mb-3"
			>
				{form.error}
			</div>
		{/if}

		{#if !showDeleteConfirm}
			<button on:click={() => (showDeleteConfirm = true)} class="btn-danger text-sm">
				Delete My Account
			</button>
		{:else}
			<form method="POST" action="?/deleteAccount" class="space-y-3">
				<p class="text-red-400 text-sm font-medium">Confirm account deletion:</p>
				<div>
					<label for="delete-password" class="label">Enter your password to confirm</label>
					<input
						id="delete-password"
						name="password"
						type="password"
						required
						class="input border-red-900"
						placeholder="Your password"
					/>
				</div>
				<div class="flex gap-3">
					<button type="submit" class="btn-danger text-sm">Yes, Delete My Account</button>
					<button
						type="button"
						on:click={() => (showDeleteConfirm = false)}
						class="btn-secondary text-sm">Cancel</button
					>
				</div>
			</form>
		{/if}
	</div>
</div>
