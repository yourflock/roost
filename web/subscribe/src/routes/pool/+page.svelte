<script lang="ts">
	import { onMount } from 'svelte';

	interface PoolGroup {
		id: string;
		name: string;
		invite_code: string;
		owner_family_id: string;
		max_members: number;
		member_count: number;
		created_at: string;
	}

	interface PoolSource {
		id: string;
		pool_id: string;
		family_id: string;
		source_url: string;
		source_type: string;
		health_score: number;
		last_checked_at: string;
		created_at: string;
	}

	type View = 'list' | 'detail';
	let view = $state<View>('list');
	let pools = $state<PoolGroup[]>([]);
	let selectedPool = $state<PoolGroup | null>(null);
	let sources = $state<PoolSource[]>([]);
	let loadingPools = $state(true);
	let loadingSources = $state(false);
	let poolsError = $state('');
	let sourcesError = $state('');

	// Create pool form
	let showCreateModal = $state(false);
	let createName = $state('');
	let createMaxMembers = $state(10);
	let creating = $state(false);
	let createError = $state('');

	// Join pool form
	let showJoinModal = $state(false);
	let joinCode = $state('');
	let joining = $state(false);
	let joinError = $state('');
	let joinSuccess = $state('');

	// Add source form
	let showAddSourceModal = $state(false);
	let addSourceURL = $state('');
	let addSourceType = $state('iptv');
	let addingSource = $state(false);
	let addSourceError = $state('');

	// Health check
	let checkingHealth = $state(false);
	let healthJobID = $state('');

	// Copy invite code
	let copiedCode = $state(false);

	async function loadPools() {
		loadingPools = true;
		poolsError = '';
		try {
			const res = await fetch('/api/pool/groups');
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			pools = await res.json();
		} catch (e) {
			poolsError = e instanceof Error ? e.message : 'Failed to load pools';
		} finally {
			loadingPools = false;
		}
	}

	async function loadSources(poolID: string) {
		loadingSources = true;
		sourcesError = '';
		try {
			const res = await fetch(`/api/pool/groups/${poolID}/sources`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			sources = await res.json();
		} catch (e) {
			sourcesError = e instanceof Error ? e.message : 'Failed to load sources';
		} finally {
			loadingSources = false;
		}
	}

	async function openPool(pool: PoolGroup) {
		selectedPool = pool;
		view = 'detail';
		await loadSources(pool.id);
	}

	function goBack() {
		view = 'list';
		selectedPool = null;
		sources = [];
		healthJobID = '';
	}

	async function createPool() {
		if (!createName.trim()) {
			createError = 'Pool name is required';
			return;
		}
		creating = true;
		createError = '';
		try {
			const res = await fetch('/api/pool/groups', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ name: createName, max_members: createMaxMembers })
			});
			if (!res.ok) {
				const data = await res.json();
				throw new Error(data.message || `HTTP ${res.status}`);
			}
			showCreateModal = false;
			createName = '';
			createMaxMembers = 10;
			await loadPools();
		} catch (e) {
			createError = e instanceof Error ? e.message : 'Failed to create pool';
		} finally {
			creating = false;
		}
	}

	async function joinPool() {
		if (!joinCode.trim()) {
			joinError = 'Invite code is required';
			return;
		}
		joining = true;
		joinError = '';
		joinSuccess = '';
		try {
			const res = await fetch('/api/pool/join', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ invite_code: joinCode })
			});
			const data = await res.json();
			if (!res.ok) throw new Error(data.message || `HTTP ${res.status}`);
			joinSuccess = `Joined pool! Pool ID: ${data.pool_id}`;
			joinCode = '';
			await loadPools();
		} catch (e) {
			joinError = e instanceof Error ? e.message : 'Failed to join pool';
		} finally {
			joining = false;
		}
	}

	async function leavePool(poolID: string) {
		try {
			await fetch(`/api/pool/leave/${poolID}`, { method: 'POST' });
			goBack();
			await loadPools();
		} catch {
			// ignore
		}
	}

	async function addSource() {
		if (!addSourceURL.trim() || !selectedPool) {
			addSourceError = 'Source URL is required';
			return;
		}
		addingSource = true;
		addSourceError = '';
		try {
			const res = await fetch(`/api/pool/groups/${selectedPool.id}/sources`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ source_url: addSourceURL, source_type: addSourceType })
			});
			if (!res.ok) {
				const data = await res.json();
				throw new Error(data.message || `HTTP ${res.status}`);
			}
			showAddSourceModal = false;
			addSourceURL = '';
			addSourceType = 'iptv';
			await loadSources(selectedPool.id);
		} catch (e) {
			addSourceError = e instanceof Error ? e.message : 'Failed to add source';
		} finally {
			addingSource = false;
		}
	}

	async function removeSource(sourceID: string) {
		if (!selectedPool) return;
		try {
			await fetch(`/api/pool/groups/${selectedPool.id}/sources/${sourceID}`, { method: 'DELETE' });
			await loadSources(selectedPool.id);
		} catch {
			// ignore
		}
	}

	async function runHealthCheck() {
		if (!selectedPool) return;
		checkingHealth = true;
		healthJobID = '';
		try {
			const res = await fetch(`/api/pool/groups/${selectedPool.id}/health-check`, {
				method: 'POST'
			});
			if (res.ok) {
				const data = await res.json();
				healthJobID = data.job_id || '';
				setTimeout(() => loadSources(selectedPool!.id), 4000);
			}
		} catch {
			// ignore
		} finally {
			checkingHealth = false;
		}
	}

	async function copyCode(code: string) {
		try {
			await navigator.clipboard.writeText(code);
			copiedCode = true;
			setTimeout(() => {
				copiedCode = false;
			}, 2000);
		} catch {
			// ignore
		}
	}

	function healthColor(score: number): string {
		if (score >= 0.8) return 'text-green-400';
		if (score >= 0.4) return 'text-yellow-400';
		return 'text-red-400';
	}

	function healthLabel(score: number): string {
		if (score >= 0.8) return 'Healthy';
		if (score >= 0.4) return 'Degraded';
		return 'Down';
	}

	function formatDate(dateStr: string): string {
		if (!dateStr) return '—';
		return new Date(dateStr).toLocaleDateString('en-US', {
			month: 'short',
			day: 'numeric',
			year: 'numeric'
		});
	}

	onMount(loadPools);
</script>

<svelte:head>
	<title>Neighborhood Pool — Roost</title>
</svelte:head>

<!-- Create pool modal -->
{#if showCreateModal}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-md">
			<h2 class="text-lg font-semibold text-white mb-4">Create Pool</h2>
			<p class="text-slate-400 text-sm mb-4">
				A pool lets your neighborhood share media sources. Members contribute IPTV accounts, NAS
				servers, or VPS instances.
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
					<label class="block text-slate-400 text-sm mb-1" for="pool-name">Pool Name</label>
					<input
						id="pool-name"
						type="text"
						bind:value={createName}
						placeholder="Maple Street Media Pool"
						class="input w-full"
					/>
				</div>
				<div>
					<label class="block text-slate-400 text-sm mb-1" for="pool-max">Max Members</label>
					<input
						id="pool-max"
						type="number"
						min="2"
						max="50"
						bind:value={createMaxMembers}
						class="input w-full"
					/>
				</div>
			</div>

			<div class="flex gap-3 mt-6">
				<button class="btn-primary flex-1" onclick={createPool} disabled={creating}>
					{creating ? 'Creating…' : 'Create Pool'}
				</button>
				<button
					class="btn-secondary"
					onclick={() => {
						showCreateModal = false;
						createError = '';
					}}>Cancel</button
				>
			</div>
		</div>
	</div>
{/if}

<!-- Join pool modal -->
{#if showJoinModal}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-sm">
			<h2 class="text-lg font-semibold text-white mb-4">Join a Pool</h2>

			{#if joinSuccess}
				<div
					class="bg-green-500/10 border border-green-500/30 text-green-400 text-sm px-3 py-2 rounded-lg mb-4"
				>
					{joinSuccess}
				</div>
				<button
					class="btn-secondary w-full"
					onclick={() => {
						showJoinModal = false;
						joinSuccess = '';
					}}>Close</button
				>
			{:else}
				{#if joinError}
					<div
						class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-3 py-2 rounded-lg mb-4"
					>
						{joinError}
					</div>
				{/if}
				<div class="mb-4">
					<label class="block text-slate-400 text-sm mb-1" for="join-code">Invite Code</label>
					<input
						id="join-code"
						type="text"
						bind:value={joinCode}
						placeholder="e.g. a1b2c3d4e5f6"
						class="input w-full font-mono"
					/>
				</div>
				<div class="flex gap-3">
					<button class="btn-primary flex-1" onclick={joinPool} disabled={joining}>
						{joining ? 'Joining…' : 'Join Pool'}
					</button>
					<button
						class="btn-secondary"
						onclick={() => {
							showJoinModal = false;
							joinError = '';
						}}>Cancel</button
					>
				</div>
			{/if}
		</div>
	</div>
{/if}

<!-- Add source modal -->
{#if showAddSourceModal}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-md">
			<h2 class="text-lg font-semibold text-white mb-4">Add Source</h2>

			{#if addSourceError}
				<div
					class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-3 py-2 rounded-lg mb-4"
				>
					{addSourceError}
				</div>
			{/if}

			<div class="space-y-4">
				<div>
					<label class="block text-slate-400 text-sm mb-1" for="source-url">Source URL</label>
					<input
						id="source-url"
						type="url"
						bind:value={addSourceURL}
						placeholder="http://…/playlist.m3u8"
						class="input w-full"
					/>
				</div>
				<div>
					<label class="block text-slate-400 text-sm mb-1" for="source-type">Source Type</label>
					<select id="source-type" bind:value={addSourceType} class="input w-full">
						<option value="iptv">IPTV</option>
						<option value="nas">NAS (Synology / QNAP)</option>
						<option value="vps">VPS Owl Instance</option>
						<option value="roost">Roost Server</option>
					</select>
				</div>
			</div>

			<div class="flex gap-3 mt-6">
				<button class="btn-primary flex-1" onclick={addSource} disabled={addingSource}>
					{addingSource ? 'Adding…' : 'Add Source'}
				</button>
				<button
					class="btn-secondary"
					onclick={() => {
						showAddSourceModal = false;
						addSourceError = '';
					}}>Cancel</button
				>
			</div>
		</div>
	</div>
{/if}

<div class="max-w-4xl mx-auto px-4 py-10">
	<!-- List view -->
	{#if view === 'list'}
		<div class="flex items-center justify-between mb-8">
			<div>
				<h1 class="text-2xl font-bold text-white">Neighborhood Pool</h1>
				<p class="text-slate-400 text-sm mt-1">Share media sources with trusted neighbors</p>
			</div>
			<div class="flex gap-3">
				<button class="btn-secondary btn-sm" onclick={() => (showJoinModal = true)}
					>Join Pool</button
				>
				<button class="btn-primary btn-sm" onclick={() => (showCreateModal = true)}>New Pool</button
				>
			</div>
		</div>

		{#if poolsError}
			<div
				class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-6"
			>
				{poolsError}
			</div>
		{/if}

		{#if loadingPools}
			<div class="card text-center py-12">
				<p class="text-slate-400">Loading pools…</p>
			</div>
		{:else if pools.length === 0}
			<div class="card text-center py-16">
				<div class="text-5xl mb-4">🏘️</div>
				<h2 class="text-lg font-semibold text-white mb-2">No pools yet</h2>
				<p class="text-slate-400 text-sm mb-6">
					Create a pool to share media sources with neighbors, or join one with an invite code.
				</p>
				<div class="flex gap-3 justify-center">
					<button class="btn-primary" onclick={() => (showCreateModal = true)}>Create Pool</button>
					<button class="btn-secondary" onclick={() => (showJoinModal = true)}
						>Join with Code</button
					>
				</div>
			</div>
		{:else}
			<div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
				{#each pools as pool}
					<button
						class="card text-left hover:border-roost-500/40 transition-colors group"
						onclick={() => openPool(pool)}
					>
						<div class="flex items-start justify-between mb-2">
							<h3 class="font-semibold text-white group-hover:text-roost-300 transition-colors">
								{pool.name}
							</h3>
							<span class="text-xs text-slate-500">
								{pool.member_count}/{pool.max_members}
							</span>
						</div>
						<p class="text-slate-400 text-sm mb-3">
							{pool.member_count} member{pool.member_count !== 1 ? 's' : ''} · Joined {formatDate(
								pool.created_at
							)}
						</p>
						<div class="flex items-center gap-2">
							<code class="text-xs text-slate-500 font-mono">{pool.invite_code}</code>
						</div>
					</button>
				{/each}
			</div>
		{/if}

		<!-- Detail view -->
	{:else if view === 'detail' && selectedPool}
		<div class="flex items-center gap-3 mb-6">
			<button class="text-slate-400 hover:text-white transition-colors" onclick={goBack}>
				← Back
			</button>
			<div class="h-4 w-px bg-slate-700"></div>
			<h1 class="text-xl font-bold text-white">{selectedPool.name}</h1>
		</div>

		<!-- Pool info card -->
		<div class="card mb-6">
			<div class="flex items-start justify-between mb-3">
				<div>
					<p class="text-slate-400 text-sm">Invite Code</p>
					<div class="flex items-center gap-2 mt-1">
						<code class="text-white font-mono text-sm">{selectedPool.invite_code}</code>
						<button
							class="text-xs text-roost-400 hover:text-roost-300 transition-colors"
							onclick={() => copyCode(selectedPool!.invite_code)}
						>
							{copiedCode ? 'Copied!' : 'Copy'}
						</button>
					</div>
				</div>
				<div class="text-right text-sm">
					<p class="text-slate-400">Members</p>
					<p class="text-white font-medium">
						{selectedPool.member_count} / {selectedPool.max_members}
					</p>
				</div>
			</div>

			<div class="flex gap-2 mt-4">
				<button class="btn-secondary btn-sm" onclick={() => (showAddSourceModal = true)}>
					Add Source
				</button>
				<button class="btn-secondary btn-sm" onclick={runHealthCheck} disabled={checkingHealth}>
					{checkingHealth ? 'Checking…' : 'Health Check'}
				</button>
				<button class="btn-danger btn-sm" onclick={() => leavePool(selectedPool!.id)}>
					Leave Pool
				</button>
			</div>

			{#if healthJobID}
				<p class="text-xs text-blue-300 mt-3">Health check running… results will appear shortly.</p>
			{/if}
		</div>

		<!-- Sources -->
		<h2 class="text-base font-semibold text-slate-100 mb-3">Sources</h2>

		{#if sourcesError}
			<div
				class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-4"
			>
				{sourcesError}
			</div>
		{/if}

		{#if loadingSources}
			<div class="card text-center py-8">
				<p class="text-slate-400 text-sm">Loading sources…</p>
			</div>
		{:else if sources.length === 0}
			<div class="card text-center py-10">
				<div class="text-3xl mb-2">📡</div>
				<p class="text-slate-400 text-sm">
					No sources yet. Add your first source to contribute to the pool.
				</p>
				<button class="btn-primary btn-sm mt-4" onclick={() => (showAddSourceModal = true)}>
					Add Source
				</button>
			</div>
		{:else}
			<div class="space-y-2">
				{#each sources as source}
					<div class="card flex items-center gap-4">
						<div class="flex-1 min-w-0">
							<div class="flex items-center gap-2 mb-0.5">
								<span
									class="text-xs bg-slate-700 text-slate-300 px-1.5 py-0.5 rounded uppercase font-mono"
								>
									{source.source_type}
								</span>
								<span class="text-xs {healthColor(source.health_score)} font-medium">
									{healthLabel(source.health_score)}
								</span>
							</div>
							<p class="text-slate-300 text-sm truncate">{source.source_url}</p>
							{#if source.last_checked_at}
								<p class="text-slate-500 text-xs mt-0.5">
									Last checked: {formatDate(source.last_checked_at)}
								</p>
							{/if}
						</div>

						<!-- Health score bar -->
						<div class="shrink-0 w-16">
							<div class="h-1.5 bg-slate-700 rounded-full overflow-hidden">
								<div
									class="h-full rounded-full transition-all
										{source.health_score >= 0.8
										? 'bg-green-400'
										: source.health_score >= 0.4
											? 'bg-yellow-400'
											: 'bg-red-400'}"
									style="width: {Math.round(source.health_score * 100)}%"
								></div>
							</div>
							<p class="text-xs text-slate-500 text-right mt-0.5">
								{Math.round(source.health_score * 100)}%
							</p>
						</div>

						<button
							class="shrink-0 text-xs text-red-400 hover:text-red-300 transition-colors"
							onclick={() => removeSource(source.id)}
						>
							Remove
						</button>
					</div>
				{/each}
			</div>
		{/if}
	{/if}
</div>
