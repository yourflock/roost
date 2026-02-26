<script lang="ts">
	import { onMount } from 'svelte';

	interface BroadcastSession {
		id: string;
		family_id: string;
		stream_key: string;
		title: string;
		status: 'idle' | 'live' | 'ended';
		hls_manifest_key: string;
		viewer_count: number;
		started_at: string | null;
		ended_at: string | null;
		created_at: string;
	}

	let sessions = $state<BroadcastSession[]>([]);
	let loading = $state(true);
	let error = $state('');

	let showCreateModal = $state(false);
	let createTitle = $state('');
	let createFamilyID = $state('');
	let creating = $state(false);
	let createError = $state('');

	let copyFeedback = $state<Record<string, boolean>>({});

	async function loadSessions() {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/broadcast/sessions');
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			sessions = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load sessions';
		} finally {
			loading = false;
		}
	}

	async function createSession() {
		if (!createTitle.trim() || !createFamilyID.trim()) {
			createError = 'Title and Family ID are required';
			return;
		}
		creating = true;
		createError = '';
		try {
			const res = await fetch('/api/broadcast/sessions', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'X-Family-ID': createFamilyID,
					'X-User-ID': 'admin'
				},
				body: JSON.stringify({ title: createTitle })
			});
			if (!res.ok) {
				const data = await res.json();
				throw new Error(data.message || `HTTP ${res.status}`);
			}
			showCreateModal = false;
			createTitle = '';
			createFamilyID = '';
			await loadSessions();
		} catch (e) {
			createError = e instanceof Error ? e.message : 'Failed to create session';
		} finally {
			creating = false;
		}
	}

	async function terminateSession(id: string) {
		const sess = sessions.find((s) => s.id === id);
		if (!sess) return;
		try {
			await fetch(`/api/broadcast/sessions/${id}/end`, {
				method: 'POST',
				headers: { 'X-Family-ID': sess.family_id, 'X-User-ID': 'admin' }
			});
			await loadSessions();
		} catch {
			// ignore
		}
	}

	async function copyStreamKey(key: string, id: string) {
		try {
			await navigator.clipboard.writeText(key);
			copyFeedback[id] = true;
			setTimeout(() => {
				copyFeedback[id] = false;
			}, 2000);
		} catch {
			// clipboard not available
		}
	}

	function formatDuration(startedAt: string | null): string {
		if (!startedAt) return '—';
		const ms = Date.now() - new Date(startedAt).getTime();
		const mins = Math.floor(ms / 60000);
		const hrs = Math.floor(mins / 60);
		if (hrs > 0) return `${hrs}h ${mins % 60}m`;
		return `${mins}m`;
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-US', {
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	onMount(loadSessions);
</script>

<svelte:head>
	<title>Broadcast Studio — Roost Admin</title>
</svelte:head>

<!-- Create session modal -->
{#if showCreateModal}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-md">
			<h2 class="text-lg font-semibold text-white mb-4">Create Broadcast Session</h2>

			{#if createError}
				<div
					class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-3 py-2 rounded-lg mb-4"
				>
					{createError}
				</div>
			{/if}

			<div class="space-y-4">
				<div>
					<label class="block text-slate-400 text-sm mb-1" for="session-title">Session Title</label>
					<input
						id="session-title"
						type="text"
						bind:value={createTitle}
						placeholder="e.g. Family Movie Night"
						class="input w-full"
					/>
				</div>
				<div>
					<label class="block text-slate-400 text-sm mb-1" for="family-id">Family ID</label>
					<input
						id="family-id"
						type="text"
						bind:value={createFamilyID}
						placeholder="UUID of the family"
						class="input w-full font-mono text-sm"
					/>
				</div>
			</div>

			<div class="flex gap-3 mt-6">
				<button class="btn-primary flex-1" onclick={createSession} disabled={creating}>
					{creating ? 'Creating…' : 'Create Session'}
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

<div class="p-6 max-w-7xl mx-auto">
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">Broadcast Studio</h1>
			<p class="text-slate-400 text-sm mt-1">Monitor and manage family live broadcast sessions</p>
		</div>
		<div class="flex gap-3">
			<button class="btn-secondary btn-sm" onclick={loadSessions} disabled={loading}>
				{loading ? 'Loading…' : 'Refresh'}
			</button>
			<button class="btn-primary btn-sm" onclick={() => (showCreateModal = true)}>
				New Session
			</button>
		</div>
	</div>

	{#if error}
		<div
			class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-6"
		>
			{error}
		</div>
	{/if}

	<!-- Stats row -->
	{#if !loading && sessions.length > 0}
		<div class="grid grid-cols-3 gap-4 mb-6">
			<div class="card text-center">
				<p class="text-2xl font-bold text-white">
					{sessions.filter((s) => s.status === 'live').length}
				</p>
				<p class="text-slate-400 text-sm mt-0.5">Live now</p>
			</div>
			<div class="card text-center">
				<p class="text-2xl font-bold text-white">
					{sessions.filter((s) => s.status === 'idle').length}
				</p>
				<p class="text-slate-400 text-sm mt-0.5">Idle</p>
			</div>
			<div class="card text-center">
				<p class="text-2xl font-bold text-white">
					{sessions.reduce((a, s) => a + s.viewer_count, 0)}
				</p>
				<p class="text-slate-400 text-sm mt-0.5">Total viewers</p>
			</div>
		</div>
	{/if}

	{#if loading}
		<div class="card text-center py-12">
			<p class="text-slate-400">Loading sessions…</p>
		</div>
	{:else if sessions.length === 0}
		<div class="card text-center py-12">
			<div class="text-4xl mb-3">📡</div>
			<p class="text-slate-300 font-medium">No broadcast sessions</p>
			<p class="text-slate-500 text-sm mt-1">
				Sessions are created by families from the Flock app.
			</p>
		</div>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-700">
			<table class="w-full text-left">
				<thead class="bg-slate-800/80 border-b border-slate-700">
					<tr>
						<th class="table-header">Session</th>
						<th class="table-header">Family</th>
						<th class="table-header">Status</th>
						<th class="table-header">Viewers</th>
						<th class="table-header">Duration</th>
						<th class="table-header">Stream Key</th>
						<th class="table-header">Created</th>
						<th class="table-header"></th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-700/50">
					{#each sessions as sess}
						<tr class="table-row">
							<td class="table-cell">
								<p class="font-medium text-slate-100 text-sm">{sess.title}</p>
								<p class="text-slate-500 text-xs font-mono mt-0.5">{sess.id.slice(0, 8)}…</p>
							</td>
							<td class="table-cell">
								<code class="text-xs text-slate-400 bg-slate-700/50 px-1.5 py-0.5 rounded">
									{sess.family_id.slice(0, 8)}…
								</code>
							</td>
							<td class="table-cell">
								{#if sess.status === 'live'}
									<span class="inline-flex items-center gap-1.5 text-xs font-medium text-green-400">
										<span class="w-1.5 h-1.5 rounded-full bg-green-400 animate-pulse"></span>
										Live
									</span>
								{:else if sess.status === 'idle'}
									<span class="inline-flex items-center gap-1.5 text-xs font-medium text-slate-400">
										<span class="w-1.5 h-1.5 rounded-full bg-slate-500"></span>
										Idle
									</span>
								{:else}
									<span class="inline-flex items-center gap-1.5 text-xs font-medium text-slate-500">
										<span class="w-1.5 h-1.5 rounded-full bg-slate-600"></span>
										Ended
									</span>
								{/if}
							</td>
							<td class="table-cell text-slate-300 text-sm">
								{sess.viewer_count}
							</td>
							<td class="table-cell text-slate-300 text-sm">
								{formatDuration(sess.started_at)}
							</td>
							<td class="table-cell">
								<div class="flex items-center gap-2">
									<code class="text-xs text-slate-400 font-mono">
										{sess.stream_key.slice(0, 12)}…
									</code>
									<button
										class="text-xs text-roost-400 hover:text-roost-300 transition-colors"
										onclick={() => copyStreamKey(sess.stream_key, sess.id)}
									>
										{copyFeedback[sess.id] ? 'Copied!' : 'Copy'}
									</button>
								</div>
							</td>
							<td class="table-cell text-slate-500 text-xs">
								{formatDate(sess.created_at)}
							</td>
							<td class="table-cell">
								{#if sess.status === 'live'}
									<button class="btn-danger btn-sm" onclick={() => terminateSession(sess.id)}>
										End
									</button>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>
