<script lang="ts">
	import { enhance } from '$app/forms';

	interface VODItem {
		id: string;
		title: string;
		slug: string;
		type: string;
		genre: string | null;
		rating: string | null;
		release_year: number | null;
		duration_seconds: number | null;
		poster_url: string | null;
		is_active: boolean;
		created_at: string;
	}

	interface SeriesItem {
		id: string;
		title: string;
		slug: string;
		genre: string | null;
		rating: string | null;
		poster_url: string | null;
		is_active: boolean;
		seasons_count: number;
	}

	interface Props {
		data: { movies: VODItem[]; series: SeriesItem[] };
	}

	let { data }: Props = $props();

	let movies = $derived(data.movies);
	let seriesList = $derived(data.series);
	let activeTab = $state<'movies' | 'series'>('movies');
	let search = $state('');

	const filteredMovies = $derived(
		movies.filter(
			(m) =>
				m.title.toLowerCase().includes(search.toLowerCase()) ||
				(m.genre ?? '').toLowerCase().includes(search.toLowerCase())
		)
	);

	const filteredSeries = $derived(
		seriesList.filter(
			(s) =>
				s.title.toLowerCase().includes(search.toLowerCase()) ||
				(s.genre ?? '').toLowerCase().includes(search.toLowerCase())
		)
	);

	function formatDuration(seconds: number | null): string {
		if (!seconds) return '—';
		const h = Math.floor(seconds / 3600);
		const m = Math.floor((seconds % 3600) / 60);
		return h > 0 ? `${h}h ${m}m` : `${m}m`;
	}
</script>

<svelte:head>
	<title>VOD Catalog — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">VOD Catalog</h1>
			<p class="text-slate-400 text-sm mt-1">
				{movies.length} movies · {seriesList.length} series
			</p>
		</div>
		<div class="flex gap-3">
			<a href="/vod/new?type=movie" class="btn-primary">+ Add Movie</a>
			<a href="/vod/new?type=series" class="btn-secondary">+ Add Series</a>
			<a href="/vod/import" class="btn-secondary">Bulk Import</a>
		</div>
	</div>

	<!-- Tabs -->
	<div class="flex gap-1 mb-4 border-b border-slate-700">
		<button
			class="px-4 py-2 text-sm font-medium rounded-t {activeTab === 'movies'
				? 'bg-slate-700 text-slate-100'
				: 'text-slate-400 hover:text-slate-200'}"
			onclick={() => (activeTab = 'movies')}
		>
			Movies ({movies.length})
		</button>
		<button
			class="px-4 py-2 text-sm font-medium rounded-t {activeTab === 'series'
				? 'bg-slate-700 text-slate-100'
				: 'text-slate-400 hover:text-slate-200'}"
			onclick={() => (activeTab = 'series')}
		>
			Series ({seriesList.length})
		</button>
	</div>

	<!-- Search -->
	<div class="mb-4">
		<input
			type="text"
			placeholder="Search by title or genre..."
			bind:value={search}
			class="input w-64"
		/>
	</div>

	<!-- Movies Table -->
	{#if activeTab === 'movies'}
		<div class="bg-slate-800 rounded-lg overflow-hidden border border-slate-700">
			<table class="w-full text-sm">
				<thead class="bg-slate-700/50">
					<tr>
						<th class="text-left p-3 text-slate-300 font-medium">Title</th>
						<th class="text-left p-3 text-slate-300 font-medium">Genre</th>
						<th class="text-left p-3 text-slate-300 font-medium">Rating</th>
						<th class="text-left p-3 text-slate-300 font-medium">Year</th>
						<th class="text-left p-3 text-slate-300 font-medium">Duration</th>
						<th class="text-left p-3 text-slate-300 font-medium">Status</th>
						<th class="text-right p-3 text-slate-300 font-medium">Actions</th>
					</tr>
				</thead>
				<tbody>
					{#each filteredMovies as movie (movie.id)}
						<tr class="border-t border-slate-700/50 hover:bg-slate-700/20">
							<td class="p-3">
								<div class="flex items-center gap-3">
									{#if movie.poster_url}
										<img
											src={movie.poster_url}
											alt={movie.title}
											class="w-8 h-12 object-cover rounded"
										/>
									{:else}
										<div
											class="w-8 h-12 bg-slate-700 rounded flex items-center justify-center text-slate-500 text-xs"
										>
											No img
										</div>
									{/if}
									<div>
										<p class="text-slate-100 font-medium">{movie.title}</p>
										<p class="text-slate-500 text-xs">{movie.slug}</p>
									</div>
								</div>
							</td>
							<td class="p-3 text-slate-300">{movie.genre ?? '—'}</td>
							<td class="p-3">
								{#if movie.rating}
									<span class="px-2 py-0.5 bg-slate-700 rounded text-xs text-slate-300">
										{movie.rating}
									</span>
								{:else}
									<span class="text-slate-500">—</span>
								{/if}
							</td>
							<td class="p-3 text-slate-300">{movie.release_year ?? '—'}</td>
							<td class="p-3 text-slate-300">{formatDuration(movie.duration_seconds)}</td>
							<td class="p-3">
								<span
									class="px-2 py-0.5 rounded-full text-xs font-medium {movie.is_active
										? 'bg-green-900/30 text-green-400'
										: 'bg-slate-700 text-slate-400'}"
								>
									{movie.is_active ? 'Active' : 'Inactive'}
								</span>
							</td>
							<td class="p-3 text-right">
								<div class="flex items-center justify-end gap-2">
									<a href="/vod/{movie.id}/edit" class="text-blue-400 hover:text-blue-300 text-xs">
										Edit
									</a>
									<form method="POST" action="?/toggleActive" use:enhance>
										<input type="hidden" name="id" value={movie.id} />
										<input type="hidden" name="type" value="movie" />
										<input type="hidden" name="is_active" value={movie.is_active} />
										<button type="submit" class="text-yellow-400 hover:text-yellow-300 text-xs">
											{movie.is_active ? 'Deactivate' : 'Activate'}
										</button>
									</form>
									<form method="POST" action="?/deleteMovie" use:enhance>
										<input type="hidden" name="id" value={movie.id} />
										<button
											type="submit"
											class="text-red-400 hover:text-red-300 text-xs"
											onclick={(e) => {
												if (!confirm(`Delete "${movie.title}"?`)) e.preventDefault();
											}}
										>
											Delete
										</button>
									</form>
								</div>
							</td>
						</tr>
					{:else}
						<tr>
							<td colspan="7" class="p-8 text-center text-slate-500">
								No movies found{search ? ` matching "${search}"` : ''}.
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	<!-- Series Table -->
	{#if activeTab === 'series'}
		<div class="bg-slate-800 rounded-lg overflow-hidden border border-slate-700">
			<table class="w-full text-sm">
				<thead class="bg-slate-700/50">
					<tr>
						<th class="text-left p-3 text-slate-300 font-medium">Title</th>
						<th class="text-left p-3 text-slate-300 font-medium">Genre</th>
						<th class="text-left p-3 text-slate-300 font-medium">Rating</th>
						<th class="text-left p-3 text-slate-300 font-medium">Seasons</th>
						<th class="text-left p-3 text-slate-300 font-medium">Status</th>
						<th class="text-right p-3 text-slate-300 font-medium">Actions</th>
					</tr>
				</thead>
				<tbody>
					{#each filteredSeries as series (series.id)}
						<tr class="border-t border-slate-700/50 hover:bg-slate-700/20">
							<td class="p-3">
								<div class="flex items-center gap-3">
									{#if series.poster_url}
										<img
											src={series.poster_url}
											alt={series.title}
											class="w-8 h-12 object-cover rounded"
										/>
									{:else}
										<div
											class="w-8 h-12 bg-slate-700 rounded flex items-center justify-center text-slate-500 text-xs"
										>
											No img
										</div>
									{/if}
									<div>
										<p class="text-slate-100 font-medium">{series.title}</p>
										<p class="text-slate-500 text-xs">{series.slug}</p>
									</div>
								</div>
							</td>
							<td class="p-3 text-slate-300">{series.genre ?? '—'}</td>
							<td class="p-3">
								{#if series.rating}
									<span class="px-2 py-0.5 bg-slate-700 rounded text-xs text-slate-300">
										{series.rating}
									</span>
								{:else}
									<span class="text-slate-500">—</span>
								{/if}
							</td>
							<td class="p-3 text-slate-300"
								>{series.seasons_count} season{series.seasons_count !== 1 ? 's' : ''}</td
							>
							<td class="p-3">
								<span
									class="px-2 py-0.5 rounded-full text-xs font-medium {series.is_active
										? 'bg-green-900/30 text-green-400'
										: 'bg-slate-700 text-slate-400'}"
								>
									{series.is_active ? 'Active' : 'Inactive'}
								</span>
							</td>
							<td class="p-3 text-right">
								<div class="flex items-center justify-end gap-2">
									<a href="/vod/{series.id}/edit" class="text-blue-400 hover:text-blue-300 text-xs">
										Manage
									</a>
									<form method="POST" action="?/toggleActive" use:enhance>
										<input type="hidden" name="id" value={series.id} />
										<input type="hidden" name="type" value="series" />
										<input type="hidden" name="is_active" value={series.is_active} />
										<button type="submit" class="text-yellow-400 hover:text-yellow-300 text-xs">
											{series.is_active ? 'Deactivate' : 'Activate'}
										</button>
									</form>
									<form method="POST" action="?/deleteSeries" use:enhance>
										<input type="hidden" name="id" value={series.id} />
										<button
											type="submit"
											class="text-red-400 hover:text-red-300 text-xs"
											onclick={(e) => {
												if (!confirm(`Delete "${series.title}" and all its seasons/episodes?`))
													e.preventDefault();
											}}
										>
											Delete
										</button>
									</form>
								</div>
							</td>
						</tr>
					{:else}
						<tr>
							<td colspan="6" class="p-8 text-center text-slate-500">
								No series found{search ? ` matching "${search}"` : ''}.
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>
