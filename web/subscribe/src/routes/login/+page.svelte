<script lang="ts">
	import type { ActionData } from './$types';
	export let form: ActionData;

	let loading = false;
</script>

<svelte:head>
	<title>Sign In — Roost</title>
</svelte:head>

<div class="min-h-[80vh] flex items-center justify-center px-4 py-16">
	<div class="w-full max-w-md">
		<div class="text-center mb-8">
			<div
				class="w-14 h-14 bg-roost-500 rounded-2xl flex items-center justify-center text-white font-bold text-2xl mx-auto mb-4"
			>
				R
			</div>
			<h1 class="text-2xl font-bold text-white">Sign in to Roost</h1>
			<p class="text-slate-400 mt-1 text-sm">Welcome back. Your channels are waiting.</p>
		</div>

		<!-- Sign in with Flock -->
		<div class="mb-4">
			<a
				href="/auth/flock/login"
				class="flex items-center justify-center gap-3 w-full py-3 px-4 bg-blue-600 hover:bg-blue-700 text-white rounded-lg font-medium transition-colors"
			>
				<svg class="w-5 h-5" viewBox="0 0 24 24" fill="currentColor">
					<path
						d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 14H9V8h2v8zm4 0h-2V8h2v8z"
					/>
				</svg>
				Sign in with Flock
			</a>
		</div>

		<div class="relative mb-4">
			<div class="absolute inset-0 flex items-center">
				<div class="w-full border-t border-slate-700"></div>
			</div>
			<div class="relative flex justify-center text-sm">
				<span class="px-2 bg-slate-900 text-slate-500">or sign in with email</span>
			</div>
		</div>

		<div class="card">
			{#if form?.error}
				<div
					class="bg-red-500/10 border border-red-500/30 rounded-lg px-4 py-3 text-red-400 text-sm mb-4"
				>
					{form.error}
				</div>
			{/if}

			<form method="POST" on:submit={() => (loading = true)} class="space-y-4">
				<div>
					<label for="email" class="label">Email</label>
					<input
						id="email"
						name="email"
						type="email"
						autocomplete="email"
						required
						class="input"
						placeholder="you@example.com"
					/>
				</div>

				<div>
					<label for="password" class="label">Password</label>
					<input
						id="password"
						name="password"
						type="password"
						autocomplete="current-password"
						required
						class="input"
						placeholder="••••••••"
					/>
				</div>

				<button type="submit" class="btn-primary w-full py-2.5" disabled={loading}>
					{loading ? 'Signing in...' : 'Sign In'}
				</button>
			</form>
		</div>

		<p class="text-center text-sm text-slate-400 mt-4">
			Don't have an account?
			<a href="/subscribe" class="text-roost-400 hover:text-roost-300 underline">Subscribe now</a>
		</p>
	</div>
</div>
