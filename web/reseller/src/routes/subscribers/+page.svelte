<!-- subscribers/+page.svelte — Subscriber management (P14-T05) -->
<script lang="ts">
	import type { PageData } from './$types';
	export let data: PageData;

	let showCreate = false;
	let createEmail = '';
	let createPassword = '';
	let createName = '';
	let createError = '';
	let createLoading = false;

	async function handleCreate() {
		if (!createEmail || !createPassword) { createError = 'Email and password required.'; return; }
		createLoading = true; createError = '';
		try {
			const res = await fetch('?/create', {
				method: 'POST',
				headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
				body: `email=${encodeURIComponent(createEmail)}&password=${encodeURIComponent(createPassword)}&display_name=${encodeURIComponent(createName)}`
			});
			if (res.redirected) { window.location.reload(); return; }
			const json = await res.json();
			if (json.type === 'failure') { createError = json.data?.error ?? 'Failed to create subscriber.'; }
			else { showCreate = false; window.location.reload(); }
		} catch { createError = 'Connection error.'; }
		finally { createLoading = false; }
	}

	async function handleCancel(subscriberId: string) {
		if (!confirm('Cancel this subscriber? This will revoke their access.')) return;
		await fetch(`?/cancel`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
			body: `subscriber_id=${encodeURIComponent(subscriberId)}`
		});
		window.location.reload();
	}

	function exportCSV() {
		const rows = [['Subscriber ID', 'Email', 'Display Name', 'Status', 'Linked At']];
		for (const s of data.subscribers) {
			rows.push([s.subscriber_id, s.email, s.display_name ?? '', s.status, s.linked_at]);
		}
		const csv = rows.map(r => r.map(v => `"${v}"`).join(',')).join('\n');
		const a = document.createElement('a');
		a.href = URL.createObjectURL(new Blob([csv], { type: 'text/csv' }));
		a.download = 'roost-subscribers.csv';
		a.click();
	}
</script>

<svelte:head>
	<title>Subscribers — Roost Reseller</title>
</svelte:head>

<div class="space-y-6">
	<div class="flex items-center justify-between">
		<div>
			<h1 class="text-2xl font-bold text-white">Subscribers</h1>
			<p class="text-slate-400 mt-1">{data.total ?? 0} total subscribers</p>
		</div>
		<div class="flex gap-2">
			<button on:click={exportCSV} class="btn-secondary">Export CSV</button>
			<button on:click={() => showCreate = true} class="btn-primary">+ Add Subscriber</button>
		</div>
	</div>

	<!-- Create modal -->
	{#if showCreate}
		<div class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 px-4">
			<div class="bg-slate-800 rounded-xl border border-slate-700 p-6 w-full max-w-md">
				<h2 class="text-lg font-semibold text-white mb-4">Add Subscriber</h2>
				{#if createError}
					<p class="text-red-400 text-sm mb-3">{createError}</p>
				{/if}
				<div class="space-y-3">
					<div>
						<label class="label">Email *</label>
						<input bind:value={createEmail} type="email" class="input" placeholder="subscriber@example.com" />
					</div>
					<div>
						<label class="label">Password *</label>
						<input bind:value={createPassword} type="password" class="input" placeholder="At least 8 characters" />
					</div>
					<div>
						<label class="label">Display Name <span class="text-slate-500 font-normal">(optional)</span></label>
						<input bind:value={createName} type="text" class="input" placeholder="John Doe" />
					</div>
				</div>
				<div class="flex gap-2 mt-5 justify-end">
					<button on:click={() => showCreate = false} class="btn-secondary">Cancel</button>
					<button on:click={handleCreate} class="btn-primary" disabled={createLoading}>
						{createLoading ? 'Creating...' : 'Create Subscriber'}
					</button>
				</div>
			</div>
		</div>
	{/if}

	<!-- Subscribers table -->
	<div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
		<table class="w-full text-sm">
			<thead>
				<tr class="border-b border-slate-700">
					<th class="px-4 py-3 text-left text-xs font-semibold text-slate-400 uppercase tracking-wider">Email</th>
					<th class="px-4 py-3 text-left text-xs font-semibold text-slate-400 uppercase tracking-wider">Name</th>
					<th class="px-4 py-3 text-left text-xs font-semibold text-slate-400 uppercase tracking-wider">Status</th>
					<th class="px-4 py-3 text-left text-xs font-semibold text-slate-400 uppercase tracking-wider">Joined</th>
					<th class="px-4 py-3 text-right text-xs font-semibold text-slate-400 uppercase tracking-wider">Actions</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-700">
				{#each data.subscribers as sub}
					<tr class="hover:bg-slate-700/30 transition-colors">
						<td class="px-4 py-3 text-slate-200">{sub.email}</td>
						<td class="px-4 py-3 text-slate-400">{sub.display_name ?? '—'}</td>
						<td class="px-4 py-3">
							<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium {sub.status === 'active' ? 'bg-green-900/50 text-green-400' : sub.status === 'cancelled' ? 'bg-red-900/50 text-red-400' : 'bg-yellow-900/50 text-yellow-400'}">
								{sub.status}
							</span>
						</td>
						<td class="px-4 py-3 text-slate-400 text-xs">{new Date(sub.linked_at).toLocaleDateString()}</td>
						<td class="px-4 py-3 text-right">
							{#if sub.status !== 'cancelled'}
								<button on:click={() => handleCancel(sub.subscriber_id)} class="text-red-400 hover:text-red-300 text-xs transition-colors">Cancel</button>
							{/if}
						</td>
					</tr>
				{/each}
				{#if data.subscribers.length === 0}
					<tr>
						<td colspan="5" class="px-4 py-8 text-center text-slate-500">No subscribers yet. Add your first subscriber above.</td>
					</tr>
				{/if}
			</tbody>
		</table>
	</div>
</div>
