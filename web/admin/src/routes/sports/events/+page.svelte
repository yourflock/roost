<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';

	interface SportsEvent {
		id: string;
		league_id: string;
		home_team_id: string | null;
		away_team_id: string | null;
		season: string;
		season_type: string;
		week: string | null;
		scheduled_time: string;
		status: string;
		home_score: number;
		away_score: number;
		period: string | null;
	}

	interface League {
		id: string;
		name: string;
		abbreviation: string;
	}

	interface Props {
		data: {
			events: SportsEvent[];
			leagues: League[];
			filterLeague: string;
			filterStatus: string;
		};
	}

	let { data }: Props = $props();

	const statusColors: Record<string, string> = {
		live: 'badge-red',
		scheduled: 'badge-blue',
		final: 'badge-gray',
		postponed: 'badge-yellow',
		cancelled: 'badge-gray'
	};

	function formatTime(iso: string): string {
		return new Date(iso).toLocaleString('en-US', {
			month: 'short',
			day: 'numeric',
			hour: 'numeric',
			minute: '2-digit'
		});
	}

	function applyFilter() {
		const params = new URLSearchParams($page.url.searchParams);
		if (league) params.set('league', league);
		else params.delete('league');
		if (status) params.set('status', status);
		else params.delete('status');
		goto(`?${params}`);
	}

	let league = $derived(data.filterLeague);
	let status = $derived(data.filterStatus);
</script>

<svelte:head>
	<title>Sports Events — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<div class="mb-6">
		<nav class="text-sm text-slate-500 mb-1">
			<a href="/sports" class="hover:text-slate-300">Sports</a>
			<span class="mx-1">/</span>
			<span>Events</span>
		</nav>
		<h1 class="text-2xl font-bold text-slate-100">Sports Events</h1>
	</div>

	<!-- Filters -->
	<div class="flex gap-3 mb-6">
		<select bind:value={league} onchange={applyFilter} class="select-input">
			<option value="">All Leagues</option>
			{#each data.leagues as l}
				<option value={l.abbreviation}>{l.abbreviation} — {l.name}</option>
			{/each}
		</select>
		<select bind:value={status} onchange={applyFilter} class="select-input">
			<option value="">All Statuses</option>
			<option value="live">Live</option>
			<option value="scheduled">Scheduled</option>
			<option value="final">Final</option>
			<option value="postponed">Postponed</option>
		</select>
	</div>

	<div class="card">
		{#if data.events.length === 0}
			<p class="text-slate-400 text-sm py-8 text-center">No events found.</p>
		{:else}
			<p class="text-xs text-slate-500 mb-4">Showing {data.events.length} events</p>
			<table class="w-full text-sm">
				<thead>
					<tr class="text-left text-slate-500 border-b border-slate-700">
						<th class="pb-3 font-medium">Matchup</th>
						<th class="pb-3 font-medium">Score</th>
						<th class="pb-3 font-medium">Season</th>
						<th class="pb-3 font-medium">Scheduled</th>
						<th class="pb-3 font-medium">Status</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-700/50">
					{#each data.events as event}
						<tr class="hover:bg-slate-700/30 transition-colors">
							<td class="py-3 text-slate-100">
								{event.home_team_id ? 'Home' : '?'} vs {event.away_team_id ? 'Away' : '?'}
								{#if event.week}<span class="text-xs text-slate-500 ml-1">Wk {event.week}</span
									>{/if}
							</td>
							<td class="py-3 font-mono text-slate-300">
								{event.home_score}–{event.away_score}
								{#if event.period}<span class="text-xs text-slate-500 ml-1">({event.period})</span
									>{/if}
							</td>
							<td class="py-3 text-slate-400"
								>{event.season} <span class="text-xs capitalize">{event.season_type}</span></td
							>
							<td class="py-3 text-slate-400 text-xs">{formatTime(event.scheduled_time)}</td>
							<td class="py-3">
								<span class="badge {statusColors[event.status] ?? 'badge-gray'}">
									{event.status}
								</span>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</div>
</div>

<style>
	.card {
		@apply bg-slate-800 rounded-xl p-5 border border-slate-700;
	}
	.select-input {
		@apply bg-slate-800 border border-slate-700 text-slate-200 text-sm rounded-lg px-3 py-2 focus:ring-1 focus:ring-roost-500 focus:outline-none;
	}
	.badge {
		@apply px-2 py-0.5 rounded-full text-xs font-medium;
	}
	.badge-red {
		@apply bg-red-900/40 text-red-400;
	}
	.badge-blue {
		@apply bg-blue-900/40 text-blue-400;
	}
	.badge-yellow {
		@apply bg-yellow-900/40 text-yellow-400;
	}
	.badge-gray {
		@apply bg-slate-700 text-slate-400;
	}
</style>
