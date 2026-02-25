<script lang="ts">
	import { enhance } from '$app/forms';
	import HealthBadge from '$lib/components/HealthBadge.svelte';
	import ConfirmModal from '$lib/components/ConfirmModal.svelte';

	interface EpgSource {
		id: string;
		name: string;
		url: string;
		format: 'xmltv' | 'm3u';
		is_active: boolean;
		last_synced_at: string | null;
		sync_status: 'idle' | 'syncing' | 'error' | 'success';
		error_message: string | null;
		channel_count: number;
	}

	interface Props {
		data: { sources: EpgSource[] };
		form: { addError?: string; addSuccess?: boolean; syncTriggered?: boolean } | null;
	}

	let { data, form }: Props = $props();

	let showAddForm = $state(false);
	let removeTarget = $state<EpgSource | null>(null);
	let removeLoading = $state(false);

	function formatDate(d: string | null): string {
		if (!d) return 'Never';
		return new Date(d).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
	}
</script>

<svelte:head>
	<title>EPG Sources — Roost Admin</title>
</svelte:head>

<ConfirmModal
	open={!!removeTarget}
	title="Remove EPG Source"
	message="Remove '{removeTarget?.name}'? Channel guide data from this source will no longer be updated."
	confirmLabel="Remove"
	danger
	loading={removeLoading}
	onconfirm={() => {
		if (!removeTarget) return;
		removeLoading = true;
		const form = document.getElementById('remove-form') as HTMLFormElement;
		const input = form.querySelector('input[name="id"]') as HTMLInputElement;
		input.value = removeTarget.id;
		form.requestSubmit();
	}}
	oncancel={() => (removeTarget = null)}
/>

<form id="remove-form" method="POST" action="?/remove" use:enhance={() => {
	return async ({ update }) => { removeLoading = false; removeTarget = null; update(); };
}}>
	<input type="hidden" name="id" value="" />
</form>

<div class="p-6 max-w-7xl mx-auto">
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">EPG Sources</h1>
			<p class="text-slate-400 text-sm mt-1">{data.sources.length} source{data.sources.length !== 1 ? 's' : ''} configured</p>
		</div>
		<button class="btn-primary" onclick={() => (showAddForm = !showAddForm)}>
			{showAddForm ? 'Cancel' : '+ Add Source'}
		</button>
	</div>

	{#if form?.addError}
		<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-4">
			{form.addError}
		</div>
	{/if}
	{#if form?.addSuccess}
		<div class="bg-green-500/10 border border-green-500/30 text-green-400 text-sm px-4 py-3 rounded-lg mb-4">
			EPG source saved.
		</div>
	{/if}
	{#if form?.syncTriggered}
		<div class="bg-blue-500/10 border border-blue-500/30 text-blue-400 text-sm px-4 py-3 rounded-lg mb-4">
			Sync triggered. Check status in a few minutes.
		</div>
	{/if}

	{#if showAddForm}
		<form method="POST" action="?/add" use:enhance class="card mb-6">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-4">Add EPG Source</h2>
			<div class="grid grid-cols-1 lg:grid-cols-3 gap-4 mb-4">
				<div>
					<label class="label" for="epg_name">Name</label>
					<input id="epg_name" name="name" type="text" class="input" placeholder="XMLTV US" required />
				</div>
				<div class="lg:col-span-1">
					<label class="label" for="epg_url">URL</label>
					<input id="epg_url" name="url" type="url" class="input" placeholder="https://..." required />
				</div>
				<div>
					<label class="label" for="epg_format">Format</label>
					<select id="epg_format" name="format" class="select" required>
						<option value="xmltv">XMLTV</option>
						<option value="m3u">M3U</option>
					</select>
				</div>
			</div>
			<button type="submit" class="btn-primary">Add Source</button>
		</form>
	{/if}

	{#if data.sources.length === 0}
		<div class="card text-center py-12">
			<div class="text-4xl mb-3">📅</div>
			<p class="text-slate-400">No EPG sources configured. Add one to enable channel guides.</p>
		</div>
	{:else}
		<div class="space-y-3">
			{#each data.sources as src}
				<div class="card">
					<div class="flex items-start justify-between">
						<div class="flex-1">
							<div class="flex items-center gap-3 mb-2">
								<h3 class="font-semibold text-slate-100">{src.name}</h3>
								<HealthBadge status={src.sync_status} />
								<span class="text-xs text-slate-500 bg-slate-700/50 px-2 py-0.5 rounded uppercase">{src.format}</span>
							</div>
							<div class="text-xs text-slate-400 mb-1">
								<span class="font-mono">{src.url}</span>
							</div>
							<div class="text-xs text-slate-500 flex gap-4">
								<span>{src.channel_count} channels</span>
								<span>Last synced: {formatDate(src.last_synced_at)}</span>
							</div>
							{#if src.error_message}
								<div class="mt-2 text-xs text-red-400 bg-red-500/10 rounded px-3 py-1.5">
									{src.error_message}
								</div>
							{/if}
						</div>
						<div class="flex items-center gap-2 ml-4">
							<form method="POST" action="?/sync" use:enhance>
								<input type="hidden" name="id" value={src.id} />
								<button type="submit" class="btn-secondary btn-sm" disabled={src.sync_status === 'syncing'}>
									{src.sync_status === 'syncing' ? 'Syncing...' : 'Sync Now'}
								</button>
							</form>
							<button class="btn-danger btn-sm" onclick={() => (removeTarget = src)}>
								Remove
							</button>
						</div>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>
