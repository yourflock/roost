<script lang="ts">
	import { onMount } from 'svelte';

	interface FranchiseOperator {
		id: string;
		operator_name: string;
		owner_user_id: string;
		stripe_account_id: string;
		subdomain: string;
		config: Record<string, unknown>;
		status: 'pending' | 'active' | 'suspended';
		created_at: string;
	}

	interface OperatorStats {
		total_subscribers: number;
		active_subscribers: number;
		cancelled_subscribers: number;
	}

	let operators = $state<FranchiseOperator[]>([]);
	let loading = $state(true);
	let error = $state('');
	let search = $state('');

	const filtered = $derived(
		operators.filter(
			(op) =>
				op.operator_name.toLowerCase().includes(search.toLowerCase()) ||
				op.subdomain.toLowerCase().includes(search.toLowerCase())
		)
	);

	let showCreateModal = $state(false);
	let createName = $state('');
	let createOwnerID = $state('');
	let createSubdomain = $state('');
	let createEmail = $state('');
	let creating = $state(false);
	let createError = $state('');
	let createSuccess = $state<{ id: string; stripe_onboard_url?: string } | null>(null);

	let configTarget = $state<FranchiseOperator | null>(null);
	let configJSON = $state('');
	let savingConfig = $state(false);
	let configError = $state('');

	let statsTarget = $state<FranchiseOperator | null>(null);
	let stats = $state<OperatorStats | null>(null);
	let loadingStats = $state(false);

	async function loadOperators() {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/franchise/operators');
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			operators = await res.json();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load operators';
		} finally {
			loading = false;
		}
	}

	async function createOperator() {
		if (!createName.trim() || !createOwnerID.trim() || !createSubdomain.trim()) {
			createError = 'Name, Owner ID, and subdomain are required';
			return;
		}
		creating = true;
		createError = '';
		createSuccess = null;
		try {
			const res = await fetch('/api/franchise/operators', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					operator_name: createName,
					owner_user_id: createOwnerID,
					subdomain: createSubdomain,
					email: createEmail || undefined
				})
			});
			if (!res.ok) {
				const data = await res.json();
				throw new Error(data.message || `HTTP ${res.status}`);
			}
			createSuccess = await res.json();
			createName = '';
			createOwnerID = '';
			createSubdomain = '';
			createEmail = '';
			await loadOperators();
		} catch (e) {
			createError = e instanceof Error ? e.message : 'Failed to create operator';
		} finally {
			creating = false;
		}
	}

	async function setStatus(id: string, action: 'suspend' | 'activate') {
		try {
			await fetch(`/api/franchise/operators/${id}/${action}`, { method: 'POST' });
			await loadOperators();
		} catch {
			// ignore
		}
	}

	function openConfig(op: FranchiseOperator) {
		configTarget = op;
		configJSON = JSON.stringify(op.config, null, 2);
		configError = '';
	}

	async function saveConfig() {
		if (!configTarget) return;
		savingConfig = true;
		configError = '';
		try {
			let parsedConfig: unknown;
			try {
				parsedConfig = JSON.parse(configJSON);
			} catch {
				configError = 'Invalid JSON';
				return;
			}
			const res = await fetch(`/api/franchise/operators/${configTarget.id}`, {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ config: parsedConfig })
			});
			if (!res.ok) {
				const data = await res.json();
				throw new Error(data.message || `HTTP ${res.status}`);
			}
			configTarget = null;
			await loadOperators();
		} catch (e) {
			configError = e instanceof Error ? e.message : 'Failed to save config';
		} finally {
			savingConfig = false;
		}
	}

	async function openStats(op: FranchiseOperator) {
		statsTarget = op;
		stats = null;
		loadingStats = true;
		try {
			const res = await fetch(`/api/franchise/stats?operator_id=${op.id}`);
			if (res.ok) {
				stats = await res.json();
			}
		} catch {
			// ignore
		} finally {
			loadingStats = false;
		}
	}

	async function getConnectLink(id: string) {
		try {
			const res = await fetch(`/api/franchise/operators/${id}/connect`);
			if (!res.ok) return;
			const data = await res.json();
			if (data.url) {
				window.open(data.url, '_blank', 'noopener');
			}
		} catch {
			// ignore
		}
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-US', {
			month: 'short', day: 'numeric', year: 'numeric'
		});
	}

	function statusColor(status: string): string {
		switch (status) {
			case 'active': return 'text-green-400';
			case 'suspended': return 'text-red-400';
			default: return 'text-yellow-400';
		}
	}

	onMount(loadOperators);
</script>

<svelte:head>
	<title>Franchise Operators — Roost Admin</title>
</svelte:head>

<!-- Create operator modal -->
{#if showCreateModal}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-md">
			<h2 class="text-lg font-semibold text-white mb-4">New Franchise Operator</h2>

			{#if createSuccess}
				<div class="bg-green-500/10 border border-green-500/30 text-green-400 text-sm px-3 py-3 rounded-lg mb-4">
					<p class="font-medium">Operator created! ID: <code>{createSuccess.id}</code></p>
					{#if createSuccess.stripe_onboard_url}
						<a href={createSuccess.stripe_onboard_url} target="_blank" rel="noopener"
							class="mt-2 block underline text-green-300">
							Complete Stripe Connect onboarding →
						</a>
					{/if}
				</div>
				<button class="btn-secondary w-full" onclick={() => { showCreateModal = false; createSuccess = null; }}>
					Close
				</button>
			{:else}
				{#if createError}
					<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-3 py-2 rounded-lg mb-4">
						{createError}
					</div>
				{/if}

				<div class="space-y-4">
					<div>
						<label class="block text-slate-400 text-sm mb-1" for="op-name">Operator Name</label>
						<input id="op-name" type="text" bind:value={createName} placeholder="Acme TV" class="input w-full" />
					</div>
					<div>
						<label class="block text-slate-400 text-sm mb-1" for="op-owner">Owner User ID</label>
						<input id="op-owner" type="text" bind:value={createOwnerID} placeholder="UUID" class="input w-full font-mono text-sm" />
					</div>
					<div>
						<label class="block text-slate-400 text-sm mb-1" for="op-subdomain">Subdomain</label>
						<div class="flex items-center">
							<input id="op-subdomain" type="text" bind:value={createSubdomain} placeholder="acme" class="input flex-1 rounded-r-none" />
							<span class="bg-slate-700 border border-slate-600 border-l-0 px-3 py-2 text-slate-400 text-sm rounded-r-lg">.yourflock.org</span>
						</div>
					</div>
					<div>
						<label class="block text-slate-400 text-sm mb-1" for="op-email">Email (for Stripe)</label>
						<input id="op-email" type="email" bind:value={createEmail} placeholder="operator@example.com" class="input w-full" />
					</div>
				</div>

				<div class="flex gap-3 mt-6">
					<button class="btn-primary flex-1" onclick={createOperator} disabled={creating}>
						{creating ? 'Creating…' : 'Create Operator'}
					</button>
					<button class="btn-secondary" onclick={() => { showCreateModal = false; createError = ''; }}>
						Cancel
					</button>
				</div>
			{/if}
		</div>
	</div>
{/if}

<!-- Config editor modal -->
{#if configTarget}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-lg">
			<h2 class="text-lg font-semibold text-white mb-1">Edit Config</h2>
			<p class="text-slate-400 text-sm mb-4">{configTarget.operator_name}</p>

			{#if configError}
				<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-3 py-2 rounded-lg mb-4">
					{configError}
				</div>
			{/if}

			<textarea
				bind:value={configJSON}
				rows="12"
				class="input w-full font-mono text-xs resize-none"
			></textarea>

			<div class="flex gap-3 mt-4">
				<button class="btn-primary flex-1" onclick={saveConfig} disabled={savingConfig}>
					{savingConfig ? 'Saving…' : 'Save Config'}
				</button>
				<button class="btn-secondary" onclick={() => (configTarget = null)}>Cancel</button>
			</div>
		</div>
	</div>
{/if}

<!-- Stats modal -->
{#if statsTarget}
	<div class="fixed inset-0 bg-black/70 flex items-center justify-center z-50 px-4">
		<div class="bg-slate-800 border border-slate-700 rounded-xl p-6 w-full max-w-sm">
			<h2 class="text-lg font-semibold text-white mb-1">Operator Stats</h2>
			<p class="text-slate-400 text-sm mb-4">{statsTarget.operator_name}</p>

			{#if loadingStats}
				<p class="text-slate-400 text-sm text-center py-4">Loading…</p>
			{:else if stats}
				<div class="grid grid-cols-3 gap-3 text-center mb-4">
					<div class="bg-slate-700/50 rounded-lg p-3">
						<p class="text-xl font-bold text-white">{stats.total_subscribers}</p>
						<p class="text-slate-400 text-xs mt-0.5">Total</p>
					</div>
					<div class="bg-slate-700/50 rounded-lg p-3">
						<p class="text-xl font-bold text-green-400">{stats.active_subscribers}</p>
						<p class="text-slate-400 text-xs mt-0.5">Active</p>
					</div>
					<div class="bg-slate-700/50 rounded-lg p-3">
						<p class="text-xl font-bold text-slate-400">{stats.cancelled_subscribers}</p>
						<p class="text-slate-400 text-xs mt-0.5">Cancelled</p>
					</div>
				</div>
			{:else}
				<p class="text-slate-500 text-sm text-center py-4">No stats available</p>
			{/if}

			<button class="btn-secondary w-full" onclick={() => (statsTarget = null)}>Close</button>
		</div>
	</div>
{/if}

<div class="p-6 max-w-7xl mx-auto">
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">Franchise Operators</h1>
			<p class="text-slate-400 text-sm mt-1">
				White-label Roost operators with their own subscribers and Stripe accounts
			</p>
		</div>
		<div class="flex gap-3">
			<button class="btn-secondary btn-sm" onclick={loadOperators}>Refresh</button>
			<button class="btn-primary btn-sm" onclick={() => (showCreateModal = true)}>New Operator</button>
		</div>
	</div>

	{#if error}
		<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-6">
			{error}
		</div>
	{/if}

	<!-- Search -->
	<div class="mb-4">
		<input
			type="search"
			bind:value={search}
			placeholder="Search operators…"
			class="input w-full max-w-sm"
		/>
	</div>

	<!-- Summary -->
	{#if !loading && operators.length > 0}
		<div class="grid grid-cols-3 gap-4 mb-6">
			<div class="card text-center">
				<p class="text-2xl font-bold text-white">{operators.length}</p>
				<p class="text-slate-400 text-sm mt-0.5">Total</p>
			</div>
			<div class="card text-center">
				<p class="text-2xl font-bold text-green-400">{operators.filter(o => o.status === 'active').length}</p>
				<p class="text-slate-400 text-sm mt-0.5">Active</p>
			</div>
			<div class="card text-center">
				<p class="text-2xl font-bold text-yellow-400">{operators.filter(o => o.status === 'pending').length}</p>
				<p class="text-slate-400 text-sm mt-0.5">Pending</p>
			</div>
		</div>
	{/if}

	{#if loading}
		<div class="card text-center py-12">
			<p class="text-slate-400">Loading operators…</p>
		</div>
	{:else if filtered.length === 0}
		<div class="card text-center py-12">
			<div class="text-4xl mb-3">🏪</div>
			<p class="text-slate-300 font-medium">
				{operators.length === 0 ? 'No franchise operators yet' : 'No results for "' + search + '"'}
			</p>
		</div>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-700">
			<table class="w-full text-left">
				<thead class="bg-slate-800/80 border-b border-slate-700">
					<tr>
						<th class="table-header">Operator</th>
						<th class="table-header">Subdomain</th>
						<th class="table-header">Status</th>
						<th class="table-header">Stripe</th>
						<th class="table-header">Created</th>
						<th class="table-header">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-700/50">
					{#each filtered as op}
						<tr class="table-row">
							<td class="table-cell">
								<p class="font-medium text-slate-100 text-sm">{op.operator_name}</p>
								<p class="text-slate-500 text-xs font-mono mt-0.5">{op.id.slice(0, 8)}…</p>
							</td>
							<td class="table-cell">
								<code class="text-xs text-slate-300">{op.subdomain}.yourflock.org</code>
							</td>
							<td class="table-cell">
								<span class="text-xs font-medium capitalize {statusColor(op.status)}">{op.status}</span>
							</td>
							<td class="table-cell">
								{#if op.stripe_account_id}
									<button
										class="text-xs text-roost-400 hover:text-roost-300"
										onclick={() => getConnectLink(op.id)}
									>
										Reconnect
									</button>
								{:else}
									<button
										class="text-xs text-yellow-400 hover:text-yellow-300"
										onclick={() => getConnectLink(op.id)}
									>
										Connect Stripe
									</button>
								{/if}
							</td>
							<td class="table-cell text-slate-500 text-xs">{formatDate(op.created_at)}</td>
							<td class="table-cell">
								<div class="flex gap-2">
									<button
										class="btn-secondary btn-sm"
										onclick={() => openStats(op)}
										title="View stats"
									>
										Stats
									</button>
									<button
										class="btn-secondary btn-sm"
										onclick={() => openConfig(op)}
										title="Edit config"
									>
										Config
									</button>
									{#if op.status === 'active'}
										<button
											class="btn-danger btn-sm"
											onclick={() => setStatus(op.id, 'suspend')}
										>
											Suspend
										</button>
									{:else if op.status === 'suspended' || op.status === 'pending'}
										<button
											class="btn-primary btn-sm"
											onclick={() => setStatus(op.id, 'activate')}
										>
											Activate
										</button>
									{/if}
								</div>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>
