<script lang="ts">
	import { onMount } from 'svelte';

	interface Clip {
		id: string;
		family_id: string;
		source_segment_key: string;
		title: string;
		duration_secs: number;
		thumbnail_key: string;
		share_count: number;
		created_at: string;
	}

	let clips = $state<Clip[]>([]);
	let loading = $state(true);
	let error = $state('');

	let showCreateModal = $state(false);
	let createSegmentURL = $state('');
	let createTitle = $state('');
	let createStartSec = $state(0);
	let createDurationSec = $state(30);
	let creating = $state(false);
	let createError = $state('');

	let deleteTarget = $state<Clip | null>(null);
	let deleting = $state(false);

	let shareResult = $state<{ url: string; expires_in: string } | null>(null);
	let sharing = $state<string | null>(null);

	async function loadClips() {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/clips');
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			clips = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load clips';
		} finally {
			loading = false;
		}
	}

	async function createClip() {
		if (!createSegmentURL.trim()) {
			createError = 'Segment URL is required';
			return;
		}
		if (createDurationSec <= 0 || createDurationSec > 300) {
			createError = 'Duration must be between 1 and 300 seconds';
			return;
		}
		creating = true;
		createError = '';
		try {
			const res = await fetch('/api/clips', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					segment_url: createSegmentURL,
					title: createTitle || undefined,
					start_sec: createStartSec,
					duration_sec: createDurationSec
				})
			});
			if (!res.ok) {
				const data = await res.json();
				throw new Error(data.message || `HTTP ${res.status}`);
			}
			showCreateModal = false;
			createSegmentURL = '';
			createTitle = '';
			createStartSec = 0;
			createDurationSec = 30;
			await loadClips();
		} catch (e) {
			createError = e instanceof Error ? e.message : 'Failed to create clip';
		} finally {
			creating = false;
		}
	}

	async function deleteClip() {
		if (!deleteTarget) return;
		deleting = true;
		try {
			await fetch(`/api/clips/${deleteTarget.id}`, { method: 'DELETE' });
			deleteTarget = null;
			await loadClips();
		} catch {
			// ignore
		} finally {
			deleting = false;
		}
	}

	async function shareClip(id: string) {
		sharing = id;
		shareResult = null;
		try {
			const res = await fetch(`/api/clips/${id}/share`, { method: 'POST' });
			if (res.ok) {
				shareResult = await res.json();
			}
		} catch {
			// ignore
		} finally {
			sharing = null;
		}
	}

	async function copyShareURL() {
		if (!shareResult) return;
		try {
			await navigator.clipboard.writeText(shareResult.url);
		} catch {
			// ignore
		}
	}

	function formatDuration(secs: number): string {
		if (secs <= 0) return '—';
		const m = Math.floor(secs / 60);
		const s = secs % 60;
		if (m > 0) return `${m}m ${s}s`;
		return `${s}s`;
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-US', {
			month: 'short',
			day: 'numeric',
			year: 'numeric'
		});
	}

	onMount(loadClips);
</script>

<svelte:head>
	<title>My Clips — Roost</title>
</svelte:head>

<!-- Create clip modal -->
{#if showCreateModal}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-md">
			<h2 class="text-lg font-semibold text-white mb-4">Create Clip</h2>
			<p class="text-slate-400 text-sm mb-4">
				Clips are cut from DVR segments and uploaded to your library. Encoding may take a few
				seconds.
			</p>

			{#if createError}
				<div
					class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-3 py-2 rounded-lg mb-4"
				>
					{createError}
				</div>
			{/if}

			<div class="space-y-4">
				<div>
					<label class="block text-slate-400 text-sm mb-1" for="clip-segment">Segment URL</label>
					<input
						id="clip-segment"
						type="url"
						bind:value={createSegmentURL}
						placeholder="https://…/segment.ts"
						class="input w-full"
					/>
				</div>
				<div>
					<label class="block text-slate-400 text-sm mb-1" for="clip-title">Title (optional)</label>
					<input
						id="clip-title"
						type="text"
						bind:value={createTitle}
						placeholder="My highlight"
						class="input w-full"
					/>
				</div>
				<div class="grid grid-cols-2 gap-3">
					<div>
						<label class="block text-slate-400 text-sm mb-1" for="clip-start">Start (seconds)</label
						>
						<input
							id="clip-start"
							type="number"
							min="0"
							bind:value={createStartSec}
							class="input w-full"
						/>
					</div>
					<div>
						<label class="block text-slate-400 text-sm mb-1" for="clip-duration"
							>Duration (seconds)</label
						>
						<input
							id="clip-duration"
							type="number"
							min="1"
							max="300"
							bind:value={createDurationSec}
							class="input w-full"
						/>
					</div>
				</div>
			</div>

			<div class="flex gap-3 mt-6">
				<button class="btn-primary flex-1" onclick={createClip} disabled={creating}>
					{creating ? 'Encoding…' : 'Create Clip'}
				</button>
				<button
					class="btn-secondary"
					onclick={() => {
						showCreateModal = false;
						createError = '';
					}}
				>
					Cancel
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Delete confirm modal -->
{#if deleteTarget}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-sm">
			<h2 class="text-lg font-semibold text-white mb-2">Delete Clip</h2>
			<p class="text-slate-400 text-sm mb-6">
				Delete "{deleteTarget.title}"? This cannot be undone.
			</p>
			<div class="flex gap-3">
				<button class="btn-danger flex-1" onclick={deleteClip} disabled={deleting}>
					{deleting ? 'Deleting…' : 'Delete'}
				</button>
				<button class="btn-secondary" onclick={() => (deleteTarget = null)}>Cancel</button>
			</div>
		</div>
	</div>
{/if}

<!-- Share URL modal -->
{#if shareResult}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-md">
			<h2 class="text-lg font-semibold text-white mb-2">Share Link</h2>
			<p class="text-slate-400 text-sm mb-4">Valid for {shareResult.expires_in}.</p>
			<div class="flex items-center gap-2 bg-slate-700/50 rounded-lg px-3 py-2 mb-4">
				<p class="text-slate-300 text-xs font-mono truncate flex-1">{shareResult.url}</p>
				<button class="text-roost-400 hover:text-roost-300 text-xs shrink-0" onclick={copyShareURL}>
					Copy
				</button>
			</div>
			<button class="btn-secondary w-full" onclick={() => (shareResult = null)}>Close</button>
		</div>
	</div>
{/if}

<div class="max-w-5xl mx-auto px-4 py-10">
	<div class="flex items-center justify-between mb-8">
		<div>
			<h1 class="text-2xl font-bold text-white">My Clips</h1>
			<p class="text-slate-400 text-sm mt-1">Short clips cut from your DVR recordings</p>
		</div>
		<button class="btn-primary btn-sm" onclick={() => (showCreateModal = true)}> New Clip </button>
	</div>

	{#if error}
		<div
			class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-6"
		>
			{error}
		</div>
	{/if}

	{#if loading}
		<div class="card text-center py-12">
			<p class="text-slate-400">Loading your clips…</p>
		</div>
	{:else if clips.length === 0}
		<div class="card text-center py-16">
			<div class="text-5xl mb-4">🎬</div>
			<h2 class="text-lg font-semibold text-white mb-2">No clips yet</h2>
			<p class="text-slate-400 text-sm mb-6">
				Cut highlights from any DVR recording and share them with your family.
			</p>
			<button class="btn-primary" onclick={() => (showCreateModal = true)}>
				Create your first clip
			</button>
		</div>
	{:else}
		<div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
			{#each clips as clip}
				<div class="card group hover:border-roost-500/40 transition-colors">
					<!-- Thumbnail -->
					<div
						class="bg-slate-700/50 rounded-lg aspect-video mb-3 flex items-center justify-center overflow-hidden"
					>
						{#if clip.thumbnail_key}
							<img
								src="/api/clips/{clip.id}/thumbnail"
								alt={clip.title}
								class="w-full h-full object-cover"
								loading="lazy"
							/>
						{:else}
							<div class="text-3xl opacity-30">🎬</div>
						{/if}
					</div>

					<h3 class="text-white font-medium text-sm truncate mb-1">{clip.title}</h3>
					<div class="flex items-center justify-between text-xs text-slate-500 mb-3">
						<span>{formatDuration(clip.duration_secs)}</span>
						<span>{formatDate(clip.created_at)}</span>
					</div>

					{#if clip.share_count > 0}
						<p class="text-xs text-slate-500 mb-3">
							Shared {clip.share_count} time{clip.share_count !== 1 ? 's' : ''}
						</p>
					{/if}

					<div class="flex gap-2">
						<button
							class="btn-secondary btn-sm flex-1"
							onclick={() => shareClip(clip.id)}
							disabled={sharing === clip.id}
						>
							{sharing === clip.id ? 'Getting link…' : 'Share'}
						</button>
						<button
							class="btn-danger btn-sm"
							onclick={() => (deleteTarget = clip)}
							aria-label="Delete clip"
						>
							Delete
						</button>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>
