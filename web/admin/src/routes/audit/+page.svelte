<!-- +page.svelte — Audit Log viewer.
     P16-T01: Structured Logging & Audit Trail
     Searchable, filterable table of all audit_log rows. Superowner only. -->
<script lang="ts">
	import { goto } from '$app/navigation';

	interface AuditEntry {
		id: string;
		actor_type: string;
		actor_id: string | null;
		action: string;
		resource_type: string;
		resource_id: string | null;
		details: Record<string, unknown>;
		ip_address: string | null;
		user_agent: string | null;
		created_at: string;
	}

	interface Props {
		data: {
			entries: AuditEntry[];
			total: number;
			page: number;
			per_page: number;
			total_pages: number;
			filters: {
				actor_id: string;
				action: string;
				resource_type: string;
				date_from: string;
				date_to: string;
			};
		};
	}

	let { data }: Props = $props();

	let actorId = $state(data.filters.actor_id);
	let action = $state(data.filters.action);
	let resourceType = $state(data.filters.resource_type);
	let dateFrom = $state(data.filters.date_from);
	let dateTo = $state(data.filters.date_to);
	let expandedId = $state<string | null>(null);

	function applyFilters() {
		const params = new URLSearchParams({ page: '1' });
		if (actorId) params.set('actor_id', actorId);
		if (action) params.set('action', action);
		if (resourceType) params.set('resource_type', resourceType);
		if (dateFrom) params.set('date_from', dateFrom);
		if (dateTo) params.set('date_to', dateTo);
		goto(`/audit?${params.toString()}`);
	}

	function changePage(p: number) {
		const params = new URLSearchParams();
		if (actorId) params.set('actor_id', actorId);
		if (action) params.set('action', action);
		if (resourceType) params.set('resource_type', resourceType);
		if (dateFrom) params.set('date_from', dateFrom);
		if (dateTo) params.set('date_to', dateTo);
		params.set('page', String(p));
		goto(`/audit?${params.toString()}`);
	}

	function formatDate(d: string): string {
		return new Date(d).toLocaleString('en-US', {
			month: 'short', day: 'numeric', year: 'numeric',
			hour: '2-digit', minute: '2-digit', second: '2-digit'
		});
	}

	function actorBadgeClass(type: string): string {
		return {
			admin: 'bg-purple-900 text-purple-200',
			subscriber: 'bg-blue-900 text-blue-200',
			system: 'bg-slate-700 text-slate-300',
			reseller: 'bg-amber-900 text-amber-200',
		}[type] ?? 'bg-slate-700 text-slate-300';
	}

	function actionColor(a: string): string {
		if (a.includes('delete') || a.includes('suspend') || a.includes('ban')) return 'text-red-400';
		if (a.includes('create') || a.includes('activate')) return 'text-green-400';
		if (a.includes('abuse')) return 'text-orange-400';
		return 'text-slate-300';
	}
</script>

<svelte:head>
	<title>Audit Log — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<div class="mb-8">
		<h1 class="text-2xl font-bold text-slate-100">Audit Log</h1>
		<p class="text-slate-400 mt-1">Immutable trail of all admin and subscriber actions — {data.total.toLocaleString()} total entries</p>
	</div>

	<!-- Filters -->
	<div class="bg-slate-800 border border-slate-700 rounded-xl p-5 mb-6">
		<div class="grid grid-cols-2 gap-4 md:grid-cols-5">
			<div>
				<label class="block text-xs text-slate-400 mb-1">Actor ID (UUID)</label>
				<input
					type="text"
					bind:value={actorId}
					placeholder="Filter by actor UUID"
					class="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-sm text-slate-200 placeholder-slate-500 focus:outline-none focus:border-roost-500"
				/>
			</div>
			<div>
				<label class="block text-xs text-slate-400 mb-1">Action</label>
				<input
					type="text"
					bind:value={action}
					placeholder="e.g. channel.create"
					class="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-sm text-slate-200 placeholder-slate-500 focus:outline-none focus:border-roost-500"
				/>
			</div>
			<div>
				<label class="block text-xs text-slate-400 mb-1">Resource Type</label>
				<select
					bind:value={resourceType}
					class="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-sm text-slate-200 focus:outline-none focus:border-roost-500"
				>
					<option value="">All types</option>
					<option value="channel">channel</option>
					<option value="subscriber">subscriber</option>
					<option value="subscription">subscription</option>
					<option value="billing">billing</option>
					<option value="reseller">reseller</option>
					<option value="promo">promo</option>
					<option value="key">key</option>
				</select>
			</div>
			<div>
				<label class="block text-xs text-slate-400 mb-1">From</label>
				<input
					type="datetime-local"
					bind:value={dateFrom}
					class="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-sm text-slate-200 focus:outline-none focus:border-roost-500"
				/>
			</div>
			<div>
				<label class="block text-xs text-slate-400 mb-1">To</label>
				<input
					type="datetime-local"
					bind:value={dateTo}
					class="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-sm text-slate-200 focus:outline-none focus:border-roost-500"
				/>
			</div>
		</div>
		<div class="mt-3 flex gap-2">
			<button onclick={applyFilters} class="px-4 py-2 bg-roost-600 hover:bg-roost-500 text-white rounded-lg text-sm font-medium transition-colors">
				Apply Filters
			</button>
			<button onclick={() => { actorId = ''; action = ''; resourceType = ''; dateFrom = ''; dateTo = ''; applyFilters(); }}
				class="px-4 py-2 bg-slate-700 hover:bg-slate-600 text-slate-300 rounded-lg text-sm transition-colors">
				Clear
			</button>
		</div>
	</div>

	<!-- Table -->
	<div class="bg-slate-800 border border-slate-700 rounded-xl overflow-hidden">
		{#if data.entries.length === 0}
			<div class="py-12 text-center text-slate-500">No audit entries found</div>
		{:else}
			<table class="w-full text-sm">
				<thead>
					<tr class="border-b border-slate-700 text-left">
						<th class="px-4 py-3 text-xs font-medium text-slate-400 uppercase tracking-wider">When</th>
						<th class="px-4 py-3 text-xs font-medium text-slate-400 uppercase tracking-wider">Actor</th>
						<th class="px-4 py-3 text-xs font-medium text-slate-400 uppercase tracking-wider">Action</th>
						<th class="px-4 py-3 text-xs font-medium text-slate-400 uppercase tracking-wider">Resource</th>
						<th class="px-4 py-3 text-xs font-medium text-slate-400 uppercase tracking-wider">IP</th>
						<th class="px-4 py-3 text-xs font-medium text-slate-400 uppercase tracking-wider w-8"></th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-700">
					{#each data.entries as entry}
						<tr class="hover:bg-slate-750 transition-colors cursor-pointer" onclick={() => expandedId = expandedId === entry.id ? null : entry.id}>
							<td class="px-4 py-3 text-slate-400 text-xs whitespace-nowrap">{formatDate(entry.created_at)}</td>
							<td class="px-4 py-3">
								<span class="inline-flex items-center gap-1.5">
									<span class="px-2 py-0.5 rounded-full text-xs font-medium {actorBadgeClass(entry.actor_type)}">{entry.actor_type}</span>
									{#if entry.actor_id}
										<span class="text-slate-500 text-xs font-mono">{entry.actor_id.slice(0, 8)}…</span>
									{/if}
								</span>
							</td>
							<td class="px-4 py-3 font-mono text-xs {actionColor(entry.action)}">{entry.action}</td>
							<td class="px-4 py-3 text-slate-400 text-xs">
								<span class="text-slate-300">{entry.resource_type}</span>
								{#if entry.resource_id}
									<span class="text-slate-500 ml-1 font-mono">{entry.resource_id.slice(0, 8)}…</span>
								{/if}
							</td>
							<td class="px-4 py-3 text-slate-500 text-xs font-mono">{entry.ip_address ?? '—'}</td>
							<td class="px-4 py-3 text-slate-500 text-xs">{expandedId === entry.id ? '▲' : '▼'}</td>
						</tr>
						{#if expandedId === entry.id}
							<tr class="bg-slate-900">
								<td colspan="6" class="px-4 py-3">
									<div class="text-xs space-y-1">
										<div><span class="text-slate-400">Full Actor ID:</span> <span class="text-slate-200 font-mono">{entry.actor_id ?? '—'}</span></div>
										<div><span class="text-slate-400">Full Resource ID:</span> <span class="text-slate-200 font-mono">{entry.resource_id ?? '—'}</span></div>
										{#if entry.user_agent}
											<div><span class="text-slate-400">User-Agent:</span> <span class="text-slate-300">{entry.user_agent}</span></div>
										{/if}
										{#if Object.keys(entry.details ?? {}).length > 0}
											<div>
												<span class="text-slate-400">Details:</span>
												<pre class="mt-1 bg-slate-800 rounded p-2 text-slate-200 overflow-x-auto">{JSON.stringify(entry.details, null, 2)}</pre>
											</div>
										{/if}
									</div>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>

			<!-- Pagination -->
			{#if data.total_pages > 1}
				<div class="px-4 py-3 border-t border-slate-700 flex items-center justify-between">
					<p class="text-sm text-slate-400">
						Page {data.page} of {data.total_pages} ({data.total.toLocaleString()} entries)
					</p>
					<div class="flex gap-2">
						{#if data.page > 1}
							<button onclick={() => changePage(data.page - 1)} class="px-3 py-1 bg-slate-700 text-slate-300 rounded text-sm hover:bg-slate-600">← Prev</button>
						{/if}
						{#if data.page < data.total_pages}
							<button onclick={() => changePage(data.page + 1)} class="px-3 py-1 bg-slate-700 text-slate-300 rounded text-sm hover:bg-slate-600">Next →</button>
						{/if}
					</div>
				</div>
			{/if}
		{/if}
	</div>
</div>
