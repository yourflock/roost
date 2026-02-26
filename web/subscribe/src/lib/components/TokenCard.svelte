<script lang="ts">
	import type { ApiToken } from '$lib/api';

	export let token: ApiToken | null = null;
	export let onRegenerate: () => Promise<void>;

	let revealed = false;
	let copied = false;
	let regenerating = false;
	let confirmRegen = false;

	function maskToken(t: string): string {
		if (t.length <= 8) return '●'.repeat(t.length);
		return t.slice(0, 4) + '●'.repeat(t.length - 8) + t.slice(-4);
	}

	async function copyToken() {
		if (!token) return;
		await navigator.clipboard.writeText(token.token);
		copied = true;
		setTimeout(() => (copied = false), 2000);
	}

	async function handleRegenerate() {
		if (!confirmRegen) {
			confirmRegen = true;
			setTimeout(() => (confirmRegen = false), 5000);
			return;
		}
		regenerating = true;
		confirmRegen = false;
		try {
			await onRegenerate();
		} finally {
			regenerating = false;
		}
	}
</script>

<div class="card">
	<div class="flex items-center justify-between mb-4">
		<h2 class="text-lg font-semibold text-slate-100">API Token</h2>
		<span class="text-xs text-slate-400">Used to connect Owl and other clients</span>
	</div>

	{#if token}
		<div class="bg-slate-900 rounded-lg p-4 font-mono text-sm mb-4 flex items-center gap-3">
			<span class="flex-1 break-all text-slate-300 select-all">
				{revealed ? token.token : maskToken(token.token)}
			</span>
			<div class="flex items-center gap-2 flex-shrink-0">
				<button
					on:click={() => (revealed = !revealed)}
					class="text-slate-400 hover:text-slate-200 text-xs underline"
				>
					{revealed ? 'Hide' : 'Reveal'}
				</button>
				<button on:click={copyToken} class="btn-secondary text-xs py-1 px-3">
					{copied ? 'Copied!' : 'Copy'}
				</button>
			</div>
		</div>

		{#if token.last_used_at}
			<p class="text-xs text-slate-500 mb-4">
				Last used: {new Date(token.last_used_at).toLocaleDateString()}
			</p>
		{:else}
			<p class="text-xs text-slate-500 mb-4">Never used — add this token to Owl to get started.</p>
		{/if}

		<button
			on:click={handleRegenerate}
			class={confirmRegen ? 'btn-danger text-sm' : 'btn-secondary text-sm'}
			disabled={regenerating}
		>
			{#if regenerating}
				Regenerating...
			{:else if confirmRegen}
				Click again to confirm (old token will stop working)
			{:else}
				Regenerate Token
			{/if}
		</button>
	{:else}
		<p class="text-slate-400 text-sm">Subscribe to get your API token.</p>
	{/if}
</div>
