<script lang="ts">
	import { enhance } from '$app/forms';
	import ConfirmModal from '$lib/components/ConfirmModal.svelte';

	interface Channel {
		id: string;
		name: string;
		slug: string;
		category: string;
		is_active: boolean;
		sort_order: number;
		logo_url: string | null;
		created_at: string;
	}

	interface Props {
		data: { channels: Channel[] };
	}

	let { data }: Props = $props();

	let channels = $state(data.channels);
	let deleteTarget = $state<Channel | null>(null);
	let deleteLoading = $state(false);
	let search = $state('');

	const filtered = $derived(
		channels.filter(
			(c) =>
				c.name.toLowerCase().includes(search.toLowerCase()) ||
				c.category.toLowerCase().includes(search.toLowerCase()) ||
				c.slug.toLowerCase().includes(search.toLowerCase())
		)
	);
</script>

<svelte:head>
	<title>Channels — Roost Admin</title>
</svelte:head>

<ConfirmModal
	open={!!deleteTarget}
	title="Delete Channel"
	message="Are you sure you want to delete '{deleteTarget?.name}'? This cannot be undone. Subscribers will lose access to this channel."
	confirmLabel="Delete Channel"
	danger
	loading={deleteLoading}
	onconfirm={() => {
		if (!deleteTarget) return;
		deleteLoading = true;
		// Form submits programmatically
		const form = document.getElementById('delete-form') as HTMLFormElement;
		const input = form.querySelector('input[name="id"]') as HTMLInputElement;
		input.value = deleteTarget.id;
		form.requestSubmit();
	}}
	oncancel={() => (deleteTarget = null)}
/>

<!-- Hidden delete form -->
<form id="delete-form" method="POST" action="?/delete" use:enhance={() => {
	return async ({ update }) => {
		deleteLoading = false;
		deleteTarget = null;
		update();
	};
}}>
	<input type="hidden" name="id" value="" />
</form>

<div class="p-6 max-w-7xl mx-auto">
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">Channels</h1>
			<p class="text-slate-400 text-sm mt-1">{channels.length} channels total</p>
		</div>
		<a href="/channels/new" class="btn-primary">+ Add Channel</a>
	</div>

	<!-- Search -->
	<div class="mb-4">
		<input
			type="search"
			class="input max-w-sm"
			placeholder="Search channels..."
			bind:value={search}
		/>
	</div>

	<!-- Table -->
	<div class="overflow-x-auto rounded-xl border border-slate-700">
		<table class="w-full text-left">
			<thead class="bg-slate-800/80 border-b border-slate-700">
				<tr>
					<th class="table-header w-10">#</th>
					<th class="table-header">Channel</th>
					<th class="table-header">Category</th>
					<th class="table-header">Slug</th>
					<th class="table-header">Status</th>
					<th class="table-header">Actions</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-700/50">
				{#if filtered.length === 0}
					<tr>
						<td colspan="6" class="table-cell text-center text-slate-500 py-10">
							{search ? 'No channels match your search.' : 'No channels yet. Add your first channel.'}
						</td>
					</tr>
				{:else}
					{#each filtered as channel}
						<tr class="table-row">
							<td class="table-cell text-slate-500">{channel.sort_order}</td>
							<td class="table-cell">
								<div class="flex items-center gap-3">
									{#if channel.logo_url}
										<img
											src={channel.logo_url}
											alt=""
											class="w-8 h-8 rounded object-contain bg-slate-700 p-1"
										/>
									{:else}
										<div class="w-8 h-8 rounded bg-slate-700 flex items-center justify-center text-slate-500 text-xs">
											TV
										</div>
									{/if}
									<span class="font-medium text-slate-100">{channel.name}</span>
								</div>
							</td>
							<td class="table-cell">
								<span class="text-slate-400 text-sm">{channel.category}</span>
							</td>
							<td class="table-cell">
								<code class="text-xs text-slate-400 bg-slate-700/50 px-2 py-0.5 rounded">
									{channel.slug}
								</code>
							</td>
							<td class="table-cell">
								<form method="POST" action="?/toggleActive" use:enhance>
									<input type="hidden" name="id" value={channel.id} />
									<input type="hidden" name="is_active" value={String(channel.is_active)} />
									<button type="submit" class="cursor-pointer">
										{#if channel.is_active}
											<span class="badge-active">Active</span>
										{:else}
											<span class="badge-suspended">Inactive</span>
										{/if}
									</button>
								</form>
							</td>
							<td class="table-cell">
								<div class="flex items-center gap-2">
									<a href="/channels/{channel.id}" class="btn-secondary btn-sm">Edit</a>
									<button
										class="btn-danger btn-sm"
										onclick={() => (deleteTarget = channel)}
									>
										Delete
									</button>
								</div>
							</td>
						</tr>
					{/each}
				{/if}
			</tbody>
		</table>
	</div>
</div>
