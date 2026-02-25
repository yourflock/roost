<script lang="ts">
	export let token: string | null = null;
	export let domain: string = 'roost.yourflock.org';

	let copiedM3U = false;
	let showQR = false;

	$: m3uUrl = token
		? `https://${domain}/owl/playlist.m3u8?token=${token}`
		: null;

	$: owlDeepLink = token
		? `owl://addons/install?url=https://${domain}/owl/manifest.json&token=${token}`
		: null;

	async function copyM3U() {
		if (!m3uUrl) return;
		await navigator.clipboard.writeText(m3uUrl);
		copiedM3U = true;
		setTimeout(() => (copiedM3U = false), 2000);
	}

	const players = [
		{
			name: 'Owl',
			steps: [
				'Open Owl → Settings → Community Addons',
				'Tap "Add Addon" → Enter your API token',
				'Roost channels appear instantly'
			],
			recommended: true
		},
		{
			name: 'TiviMate',
			steps: [
				'Open TiviMate → Add Playlist',
				'Select "M3U URL" and paste the M3U URL below',
				'Set EPG to the same URL (TiviMate auto-detects)'
			]
		},
		{
			name: 'VLC',
			steps: [
				'Open VLC → Media → Open Network Stream',
				'Paste the M3U URL and click Play'
			]
		}
	];
</script>

<div class="card">
	<div class="flex items-center justify-between mb-4">
		<h2 class="text-lg font-semibold text-slate-100">Add to Your Player</h2>
	</div>

	{#if !token}
		<p class="text-slate-400 text-sm">Subscribe to get your streaming links.</p>
	{:else}
		<!-- M3U URL -->
		<div class="mb-6">
			<p class="label">M3U Playlist URL</p>
			<div class="bg-slate-900 rounded-lg p-3 font-mono text-xs flex items-center gap-2">
				<span class="flex-1 break-all text-slate-300 select-all">{m3uUrl}</span>
				<button on:click={copyM3U} class="btn-secondary text-xs py-1 px-2 flex-shrink-0">
					{copiedM3U ? 'Copied!' : 'Copy'}
				</button>
			</div>
			<p class="text-xs text-slate-500 mt-1">Keep this URL private — it contains your API token.</p>
		</div>

		<!-- Player instructions -->
		<div class="space-y-4">
			<h3 class="text-sm font-medium text-slate-300">Setup Instructions</h3>
			{#each players as player}
				<div class="bg-slate-900 rounded-lg p-4">
					<div class="flex items-center gap-2 mb-2">
						<span class="font-medium text-slate-200">{player.name}</span>
						{#if player.recommended}
							<span class="bg-roost-500/20 text-roost-400 border border-roost-500/30 px-2 py-0.5 rounded-full text-xs">Recommended</span>
						{/if}
					</div>
					<ol class="space-y-1">
						{#each player.steps as step, i}
							<li class="text-sm text-slate-400">
								<span class="text-roost-400 font-medium">{i + 1}.</span> {step}
							</li>
						{/each}
					</ol>
				</div>
			{/each}
		</div>

		<!-- Owl deep link button -->
		{#if owlDeepLink}
			<div class="mt-4 pt-4 border-t border-slate-700">
				<a href={owlDeepLink} class="btn-primary inline-flex items-center gap-2 text-sm">
					<svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
					</svg>
					Open in Owl App
				</a>
			</div>
		{/if}
	{/if}
</div>
