<script lang="ts">
	interface FormValues {
		name?: string;
		slug?: string;
		category?: string;
		stream_url?: string;
		logo_url?: string;
		epg_id?: string;
		sort_order?: number;
	}

	interface Region {
		id: string;
		name: string;
		code: string;
	}

	interface Props {
		data: { regions?: Region[] };
		form: { error?: string; values?: FormValues } | null;
	}

	let { data, form }: Props = $props();

	// Auto-generate slug from name
	let name = $derived(form?.values?.name ?? '');
	let slug = $derived(form?.values?.slug ?? '');

	function generateSlug(n: string): string {
		return n
			.toLowerCase()
			.replace(/[^a-z0-9]+/g, '-')
			.replace(/^-|-$/g, '');
	}

	function handleNameInput(e: Event) {
		const val = (e.target as HTMLInputElement).value;
		name = val;
		if (!form?.values?.slug) {
			slug = generateSlug(val);
		}
	}
</script>

<svelte:head>
	<title>Add Channel — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-2xl mx-auto">
	<div class="mb-6">
		<a href="/channels" class="text-sm text-slate-400 hover:text-slate-200 transition-colors">
			← Back to Channels
		</a>
	</div>

	<h1 class="text-2xl font-bold text-slate-100 mb-6">Add Channel</h1>

	{#if form?.error}
		<div
			class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-6"
		>
			{form.error}
		</div>
	{/if}

	<form method="POST" class="space-y-5">
		<div class="card space-y-5">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider">Channel Info</h2>

			<div class="grid grid-cols-2 gap-4">
				<div>
					<label class="label" for="name">Channel Name *</label>
					<input
						id="name"
						name="name"
						type="text"
						class="input"
						placeholder="ESPN"
						value={name}
						oninput={handleNameInput}
						required
					/>
				</div>
				<div>
					<label class="label" for="slug">Slug *</label>
					<input
						id="slug"
						name="slug"
						type="text"
						class="input"
						placeholder="espn"
						bind:value={slug}
						required
					/>
					<p class="text-xs text-slate-500 mt-1">URL-safe identifier</p>
				</div>
			</div>

			<div class="grid grid-cols-2 gap-4">
				<div>
					<label class="label" for="category">Category *</label>
					<select id="category" name="category" class="select" required>
						<option value="">Select category</option>
						<option value="sports">Sports</option>
						<option value="news">News</option>
						<option value="entertainment">Entertainment</option>
						<option value="movies">Movies</option>
						<option value="kids">Kids</option>
						<option value="music">Music</option>
						<option value="documentary">Documentary</option>
						<option value="international">International</option>
						<option value="local">Local</option>
						<option value="other">Other</option>
					</select>
				</div>
				<div>
					<label class="label" for="sort_order">Sort Order</label>
					<input
						id="sort_order"
						name="sort_order"
						type="number"
						class="input"
						placeholder="0"
						value={form?.values?.sort_order ?? 0}
						min="0"
					/>
					<p class="text-xs text-slate-500 mt-1">Lower = higher up in list</p>
				</div>
			</div>
		</div>

		<div class="card space-y-5">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider">Stream Config</h2>

			<div>
				<label class="label" for="stream_url">Stream URL *</label>
				<input
					id="stream_url"
					name="stream_url"
					type="url"
					class="input"
					placeholder="https://..."
					value={form?.values?.stream_url ?? ''}
					required
				/>
				<p class="text-xs text-slate-500 mt-1">HLS (.m3u8) or MPEG-TS stream URL</p>
			</div>

			<div>
				<label class="label" for="logo_url">Logo URL</label>
				<input
					id="logo_url"
					name="logo_url"
					type="url"
					class="input"
					placeholder="https://..."
					value={form?.values?.logo_url ?? ''}
				/>
			</div>

			<div>
				<label class="label" for="epg_id">EPG ID</label>
				<input
					id="epg_id"
					name="epg_id"
					type="text"
					class="input"
					placeholder="ESPN.us"
					value={form?.values?.epg_id ?? ''}
				/>
				<p class="text-xs text-slate-500 mt-1">Matches against EPG source channel IDs</p>
			</div>

			<div class="flex items-center gap-3">
				<input
					id="is_active"
					name="is_active"
					type="checkbox"
					class="w-4 h-4 rounded border-slate-600 bg-slate-700 text-roost-500 focus:ring-roost-500"
					checked
				/>
				<label for="is_active" class="text-sm font-medium text-slate-300">
					Active — immediately available to subscribers
				</label>
			</div>
		</div>

		<!-- Region availability (P14-T02) -->
		<div class="card space-y-4">
			<div>
				<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-1">
					Region Availability
				</h2>
				<p class="text-xs text-slate-500">
					Select which regions this channel is available in. Leave all unchecked to make it
					available globally.
				</p>
			</div>
			<div class="grid grid-cols-2 gap-2">
				{#each data?.regions ?? [] as region}
					<label class="flex items-center gap-2 cursor-pointer group">
						<input
							type="checkbox"
							name="region_ids"
							value={region.id}
							class="w-4 h-4 rounded border-slate-600 bg-slate-700 text-roost-500 focus:ring-roost-500"
						/>
						<span class="text-sm text-slate-300 group-hover:text-white transition-colors">
							<span class="font-mono text-xs text-slate-500 mr-1">{region.code.toUpperCase()}</span>
							{region.name}
						</span>
					</label>
				{/each}
			</div>
		</div>

		<div class="flex gap-3 justify-end">
			<a href="/channels" class="btn-secondary">Cancel</a>
			<button type="submit" class="btn-primary">Create Channel</button>
		</div>
	</form>
</div>
