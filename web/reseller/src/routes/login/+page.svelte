<!-- login/+page.svelte — Reseller portal login (P14-T05) -->
<script lang="ts">
	let apiKey = '';
	let loading = false;
	let error = '';

	async function handleLogin() {
		if (!apiKey.trim()) {
			error = 'API key is required.';
			return;
		}
		loading = true;
		error = '';

		try {
			const res = await fetch('?/login', {
				method: 'POST',
				headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
				body: `api_key=${encodeURIComponent(apiKey)}`
			});
			if (res.redirected) {
				window.location.href = res.url;
				return;
			}
			const data = await res.json();
			if (data.type === 'failure') {
				error = data.data?.error ?? 'Authentication failed.';
			}
		} catch {
			error = 'Connection error. Please try again.';
		} finally {
			loading = false;
		}
	}
</script>

<svelte:head>
	<title>Sign In — Roost Reseller Portal</title>
</svelte:head>

<div class="min-h-screen flex items-center justify-center px-4 bg-slate-900">
	<div class="w-full max-w-sm">
		<!-- Logo -->
		<div class="text-center mb-8">
			<div class="inline-flex items-center justify-center w-12 h-12 bg-roost-500 rounded-xl mb-4">
				<span class="text-white font-bold text-lg">R</span>
			</div>
			<h1 class="text-2xl font-bold text-white">Reseller Portal</h1>
			<p class="text-slate-400 text-sm mt-1">Sign in with your Roost reseller API key.</p>
		</div>

		{#if error}
			<div class="bg-red-900/30 border border-red-800 text-red-400 text-sm px-4 py-3 rounded-lg mb-4">
				{error}
			</div>
		{/if}

		<form method="POST" action="?/login" class="space-y-4">
			<div>
				<label for="api_key" class="label">API Key</label>
				<input
					id="api_key"
					name="api_key"
					type="password"
					class="input"
					placeholder="reseller_..."
					bind:value={apiKey}
					autocomplete="current-password"
					required
				/>
				<p class="text-xs text-slate-500 mt-1">Your key starts with <code class="font-mono text-roost-400">reseller_</code></p>
			</div>

			<button type="submit" class="btn-primary w-full" disabled={loading}>
				{loading ? 'Signing in...' : 'Sign In'}
			</button>
		</form>
	</div>
</div>
