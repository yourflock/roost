<script lang="ts">
	interface EphemeralLink {
		id: string;
		content_id: string;
		content_type: 'live' | 'vod';
		max_concurrent: number;
		view_count: number;
		expiresAt: string;
		created_at: string;
	}

	interface NewLink {
		share_url: string;
		expires_at: string;
		max_concurrent: number;
		content_id: string;
	}

	let links: EphemeralLink[] = $state([]);
	let loading = $state(true);
	let errorMsg = $state('');

	// Create link modal
	let showCreate = $state(false);
	let contentIDInput = $state('');
	let contentType = $state<'live' | 'vod'>('vod');
	let durationHours = $state(24);
	let maxConcurrent = $state(1);
	let creating = $state(false);
	let createError = $state('');
	let newLink: NewLink | null = $state(null);

	// Copy state
	let copied = $state(false);

	const DURATIONS = [
		{ label: '1 hour', value: 1 },
		{ label: '4 hours', value: 4 },
		{ label: '24 hours', value: 24 },
		{ label: '3 days', value: 72 },
		{ label: '7 days', value: 168 }
	];

	function formatTimeRemaining(isoDate: string): string {
		const ms = new Date(isoDate).getTime() - Date.now();
		if (ms <= 0) return 'Expired';
		const hours = Math.floor(ms / 3600000);
		const days = Math.floor(hours / 24);
		if (days > 0) return `${days}d ${hours % 24}h remaining`;
		return `${hours}h remaining`;
	}

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleDateString('en-US', {
			month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit'
		});
	}

	async function loadLinks() {
		loading = true;
		try {
			const res = await fetch('/api/v1/ephemeral/links');
			if (res.ok) {
				links = await res.json();
			} else {
				errorMsg = 'Could not load share links.';
			}
		} catch {
			errorMsg = 'Network error.';
		} finally {
			loading = false;
		}
	}

	async function createLink() {
		if (!contentIDInput.trim()) {
			createError = 'Content ID is required.';
			return;
		}
		creating = true;
		createError = '';
		try {
			const res = await fetch('/api/v1/ephemeral/links', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					content_id: contentIDInput.trim(),
					content_type: contentType,
					expires_in_hours: durationHours,
					max_concurrent: maxConcurrent
				})
			});
			if (res.ok) {
				newLink = await res.json();
				await loadLinks();
			} else {
				const err = await res.text();
				createError = err || 'Failed to create link.';
			}
		} catch {
			createError = 'Network error.';
		} finally {
			creating = false;
		}
	}

	async function revokeLink(id: string) {
		const res = await fetch(`/api/v1/ephemeral/links/${id}`, { method: 'DELETE' });
		if (res.ok) {
			links = links.filter((l) => l.id !== id);
		}
	}

	function copyShareURL() {
		if (!newLink) return;
		navigator.clipboard.writeText(newLink.share_url);
		copied = true;
		setTimeout(() => { copied = false; }, 2000);
	}

	$effect(() => {
		loadLinks();
	});
</script>

<svelte:head>
	<title>Share Streams — Roost</title>
</svelte:head>

<div class="max-w-2xl mx-auto px-4 py-10">
	<div class="flex items-start justify-between mb-8">
		<div>
			<h1 class="text-2xl font-bold text-white">Share Streams</h1>
			<p class="text-slate-400 text-sm mt-1">
				Create time-limited share links. Recipients can watch without a Roost account.
				Stream hours count against your subscription.
			</p>
		</div>
		<button
			class="btn-primary shrink-0"
			onclick={() => { showCreate = true; newLink = null; createError = ''; }}
		>
			Create Share Link
		</button>
	</div>

	{#if errorMsg}
		<div class="bg-red-500/10 border border-red-500/30 rounded-lg px-4 py-3 text-red-400 text-sm mb-6">
			{errorMsg}
		</div>
	{/if}

	{#if loading}
		<div class="text-slate-400 text-sm">Loading...</div>
	{:else if links.length === 0}
		<div class="card text-center py-14">
			<p class="text-slate-400 text-sm">No active share links.</p>
			<p class="text-slate-500 text-xs mt-1">Share links expire automatically. Recipients stream directly — no account needed.</p>
		</div>
	{:else}
		<div class="space-y-3">
			{#each links as link (link.id)}
				<div class="card">
					<div class="flex items-start justify-between gap-4">
						<div class="min-w-0">
							<div class="flex items-center gap-2 mb-1">
								<span class="text-xs font-medium px-2 py-0.5 rounded-full {link.content_type === 'live' ? 'bg-red-500/20 text-red-300' : 'bg-indigo-500/20 text-indigo-300'}">
									{link.content_type.toUpperCase()}
								</span>
								<span class="text-white text-sm font-medium font-mono truncate">{link.content_id}</span>
							</div>
							<div class="text-slate-500 text-xs space-x-2">
								<span>{formatTimeRemaining(link.expiresAt)}</span>
								<span>&middot;</span>
								<span>{link.view_count} view{link.view_count !== 1 ? 's' : ''}</span>
								<span>&middot;</span>
								<span>max {link.max_concurrent} concurrent</span>
								<span>&middot;</span>
								<span>created {formatDate(link.created_at)}</span>
							</div>
						</div>
						<button
							class="text-xs text-red-400 hover:text-red-300 shrink-0 transition-colors"
							onclick={() => revokeLink(link.id)}
						>
							Revoke
						</button>
					</div>
				</div>
			{/each}
		</div>
	{/if}

	<p class="text-slate-600 text-xs mt-8">
		Share links are ephemeral. Once expired or revoked, they cannot be recovered.
		Recipients must start watching before the link expires.
	</p>
</div>

<!-- Create Link Modal -->
{#if showCreate}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 p-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl w-full max-w-md p-6">

			{#if newLink}
				<!-- Success state — show generated link -->
				<h2 class="text-lg font-semibold text-white mb-4">Share Link Ready</h2>

				<div class="bg-slate-900 rounded-lg p-4 mb-4">
					<p class="text-slate-400 text-xs mb-1">Share URL</p>
					<p class="font-mono text-xs text-indigo-300 break-all">{newLink.share_url}</p>
				</div>

				<div class="text-slate-500 text-xs space-y-1 mb-5">
					<p>Expires: {formatDate(newLink.expires_at)}</p>
					<p>Max concurrent viewers: {newLink.max_concurrent}</p>
				</div>

				<div class="bg-amber-500/10 border border-amber-500/20 rounded-lg px-3 py-2 text-amber-300 text-xs mb-5">
					Stream hours count against your subscription. Recipients do not need a Roost account.
				</div>

				<!-- QR code placeholder -->
				<div class="bg-white rounded-lg p-4 flex items-center justify-center mb-5" style="height: 140px">
					<p class="text-slate-500 text-xs text-center">QR code generation coming soon.<br/>Copy the URL above to share.</p>
				</div>

				<div class="flex gap-3">
					<button class="btn-primary flex-1" onclick={copyShareURL}>
						{copied ? 'Copied!' : 'Copy URL'}
					</button>
					<button
						class="btn-secondary flex-1"
						onclick={() => { showCreate = false; newLink = null; }}
					>
						Done
					</button>
				</div>

			{:else}
				<!-- Create form -->
				<h2 class="text-lg font-semibold text-white mb-4">Create Share Link</h2>

				{#if createError}
					<div class="bg-red-500/10 border border-red-500/30 rounded-lg px-3 py-2 text-red-400 text-sm mb-4">
						{createError}
					</div>
				{/if}

				<div class="space-y-4">
					<div>
						<label for="content-id" class="label">Content ID</label>
						<input
							id="content-id"
							type="text"
							bind:value={contentIDInput}
							class="input font-mono text-sm"
							placeholder="imdb:tt1375666 or channel UUID"
						/>
					</div>

					<div>
						<label class="label">Content Type</label>
						<div class="flex gap-2">
							<button
								class="flex-1 py-2 rounded-lg text-sm font-medium border transition-colors {contentType === 'vod' ? 'bg-indigo-600 border-indigo-500 text-white' : 'bg-slate-700 border-slate-600 text-slate-300 hover:bg-slate-600'}"
								onclick={() => { contentType = 'vod'; }}
							>
								VOD
							</button>
							<button
								class="flex-1 py-2 rounded-lg text-sm font-medium border transition-colors {contentType === 'live' ? 'bg-red-600 border-red-500 text-white' : 'bg-slate-700 border-slate-600 text-slate-300 hover:bg-slate-600'}"
								onclick={() => { contentType = 'live'; }}
							>
								Live
							</button>
						</div>
					</div>

					<div>
						<label for="duration" class="label">Link Duration</label>
						<select id="duration" bind:value={durationHours} class="input">
							{#each DURATIONS as d (d.value)}
								<option value={d.value}>{d.label}</option>
							{/each}
						</select>
					</div>

					<div>
						<label for="concurrent" class="label">Max Concurrent Viewers</label>
						<select id="concurrent" bind:value={maxConcurrent} class="input">
							<option value={1}>1 viewer</option>
							<option value={2}>2 viewers</option>
							<option value={3}>3 viewers</option>
						</select>
					</div>

					<p class="text-slate-500 text-xs">
						Stream hours count against your subscription.
						Recipients do not need a Roost account to watch.
					</p>
				</div>

				<div class="flex gap-3 mt-6">
					<button
						class="btn-primary flex-1"
						onclick={createLink}
						disabled={creating}
					>
						{creating ? 'Generating...' : 'Generate Link'}
					</button>
					<button
						class="btn-secondary flex-1"
						onclick={() => { showCreate = false; createError = ''; }}
						disabled={creating}
					>
						Cancel
					</button>
				</div>
			{/if}
		</div>
	</div>
{/if}
