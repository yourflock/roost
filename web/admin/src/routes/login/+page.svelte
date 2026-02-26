<script lang="ts">
	import { enhance } from '$app/forms';

	interface Props {
		form: { error?: string; email?: string } | null;
	}

	let { form }: Props = $props();

	let loading = $state(false);
</script>

<svelte:head>
	<title>Admin Login — Roost</title>
</svelte:head>

<div class="min-h-screen bg-slate-900 flex items-center justify-center p-4">
	<div class="w-full max-w-sm">
		<!-- Logo -->
		<div class="text-center mb-8">
			<div
				class="w-12 h-12 bg-roost-500 rounded-xl flex items-center justify-center text-white font-bold text-xl mx-auto mb-3"
			>
				R
			</div>
			<h1 class="text-xl font-semibold text-slate-100">Roost Admin</h1>
			<p class="text-sm text-slate-400 mt-1">Sign in to the management console</p>
		</div>

		<div class="card">
			{#if form?.error}
				<div
					class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-4"
				>
					{form.error}
				</div>
			{/if}

			<form
				method="POST"
				use:enhance={() => {
					loading = true;
					return async ({ update }) => {
						loading = false;
						update();
					};
				}}
			>
				<div class="mb-4">
					<label class="label" for="email">Email</label>
					<input
						id="email"
						name="email"
						type="email"
						class="input"
						placeholder="admin@example.com"
						value={form?.email ?? ''}
						required
						autocomplete="email"
					/>
				</div>

				<div class="mb-6">
					<label class="label" for="password">Password</label>
					<input
						id="password"
						name="password"
						type="password"
						class="input"
						placeholder="••••••••"
						required
						autocomplete="current-password"
					/>
				</div>

				<button class="btn-primary w-full" type="submit" disabled={loading}>
					{loading ? 'Signing in...' : 'Sign In'}
				</button>
			</form>
		</div>

		<p class="text-center text-xs text-slate-500 mt-4">Roost Admin Console — restricted access</p>
	</div>
</div>
