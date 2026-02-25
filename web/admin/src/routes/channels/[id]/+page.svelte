<script lang="ts">
	interface Channel {
		id: string;
		name: string;
		slug: string;
		category: string;
		stream_url: string;
		logo_url: string | null;
		epg_id: string | null;
		sort_order: number;
		is_active: boolean;
	}

	interface Props {
		data: { channel: Channel };
		form: { error?: string } | null;
	}

	let { data, form }: Props = $props();
	const ch = data.channel;
</script>

<svelte:head>
	<title>Edit {ch.name} — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-2xl mx-auto">
	<div class="mb-6">
		<a href="/channels" class="text-sm text-slate-400 hover:text-slate-200 transition-colors">
			← Back to Channels
		</a>
	</div>

	<h1 class="text-2xl font-bold text-slate-100 mb-6">Edit Channel: {ch.name}</h1>

	{#if form?.error}
		<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-6">
			{form.error}
		</div>
	{/if}

	<form method="POST" class="space-y-5">
		<div class="card space-y-5">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider">Channel Info</h2>

			<div class="grid grid-cols-2 gap-4">
				<div>
					<label class="label" for="name">Channel Name *</label>
					<input id="name" name="name" type="text" class="input" value={ch.name} required />
				</div>
				<div>
					<label class="label" for="slug">Slug *</label>
					<input id="slug" name="slug" type="text" class="input" value={ch.slug} required />
				</div>
			</div>

			<div class="grid grid-cols-2 gap-4">
				<div>
					<label class="label" for="category">Category *</label>
					<select id="category" name="category" class="select" required>
						{#each ['sports','news','entertainment','movies','kids','music','documentary','international','local','other'] as cat}
							<option value={cat} selected={ch.category === cat}>{cat.charAt(0).toUpperCase() + cat.slice(1)}</option>
						{/each}
					</select>
				</div>
				<div>
					<label class="label" for="sort_order">Sort Order</label>
					<input id="sort_order" name="sort_order" type="number" class="input" value={ch.sort_order} min="0" />
				</div>
			</div>
		</div>

		<div class="card space-y-5">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider">Stream Config</h2>

			<div>
				<label class="label" for="stream_url">Stream URL *</label>
				<input id="stream_url" name="stream_url" type="url" class="input" value={ch.stream_url} required />
			</div>

			<div>
				<label class="label" for="logo_url">Logo URL</label>
				<input id="logo_url" name="logo_url" type="url" class="input" value={ch.logo_url ?? ''} />
			</div>

			<div>
				<label class="label" for="epg_id">EPG ID</label>
				<input id="epg_id" name="epg_id" type="text" class="input" value={ch.epg_id ?? ''} />
			</div>

			<div class="flex items-center gap-3">
				<input
					id="is_active"
					name="is_active"
					type="checkbox"
					class="w-4 h-4 rounded border-slate-600 bg-slate-700 text-roost-500 focus:ring-roost-500"
					checked={ch.is_active}
				/>
				<label for="is_active" class="text-sm font-medium text-slate-300">Active</label>
			</div>
		</div>

		<div class="flex gap-3 justify-end">
			<a href="/channels" class="btn-secondary">Cancel</a>
			<button type="submit" class="btn-primary">Save Changes</button>
		</div>
	</form>
</div>
