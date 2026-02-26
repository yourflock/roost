<script lang="ts">
	interface League {
		id: string;
		name: string;
		abbreviation: string;
		sport: string;
		is_active: boolean;
	}

	interface Props {
		data: {
			leagues: League[];
			liveCount: number;
			upcomingCount: number;
			teamCount: number;
			leagueCount: number;
		};
	}

	let { data }: Props = $props();
</script>

<svelte:head>
	<title>Sports — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<!-- Header -->
	<div class="mb-8 flex items-center justify-between">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">Sports Intelligence</h1>
			<p class="text-slate-400 text-sm mt-1">Leagues, teams, events, and live scores</p>
		</div>
		<div class="flex gap-3">
			<a href="/sports/leagues" class="btn-secondary text-sm">Manage Leagues</a>
			<a href="/sports/events" class="btn-primary text-sm">View Events</a>
		</div>
	</div>

	<!-- Stats -->
	<div class="grid grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
		<div class="stat-card">
			<div class="text-3xl font-bold text-red-400">{data.liveCount}</div>
			<div class="text-sm text-slate-400 mt-1">Games Live Now</div>
		</div>
		<div class="stat-card">
			<div class="text-3xl font-bold text-blue-400">{data.upcomingCount}</div>
			<div class="text-sm text-slate-400 mt-1">Upcoming This Week</div>
		</div>
		<div class="stat-card">
			<div class="text-3xl font-bold text-slate-100">{data.teamCount}</div>
			<div class="text-sm text-slate-400 mt-1">Total Teams</div>
		</div>
		<div class="stat-card">
			<div class="text-3xl font-bold text-slate-100">{data.leagueCount}</div>
			<div class="text-sm text-slate-400 mt-1">Active Leagues</div>
		</div>
	</div>

	<!-- Leagues overview -->
	<div class="card">
		<div class="flex items-center justify-between mb-4">
			<h2 class="text-lg font-semibold text-slate-100">Leagues</h2>
			<a href="/sports/leagues" class="text-sm text-blue-400 hover:text-blue-300">View all</a>
		</div>
		{#if data.leagues.length === 0}
			<p class="text-slate-400 text-sm py-4">No leagues configured yet.</p>
		{:else}
			<div class="divide-y divide-slate-700">
				{#each data.leagues as league}
					<div class="flex items-center justify-between py-3">
						<div>
							<span class="font-medium text-slate-100">{league.name}</span>
							<span class="ml-2 text-xs text-slate-500 uppercase tracking-wide"
								>{league.abbreviation}</span
							>
						</div>
						<div class="flex items-center gap-3">
							<span class="text-xs text-slate-500 capitalize">{league.sport.replace('_', ' ')}</span
							>
							<span class="badge {league.is_active ? 'badge-green' : 'badge-gray'}">
								{league.is_active ? 'Active' : 'Inactive'}
							</span>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>
</div>

<style>
	.stat-card {
		@apply bg-slate-800 rounded-xl p-5 border border-slate-700;
	}
	.card {
		@apply bg-slate-800 rounded-xl p-5 border border-slate-700;
	}
	.btn-primary {
		@apply inline-flex items-center px-4 py-2 bg-roost-500 hover:bg-roost-600 text-white rounded-lg font-medium transition-colors;
	}
	.btn-secondary {
		@apply inline-flex items-center px-4 py-2 bg-slate-700 hover:bg-slate-600 text-slate-100 rounded-lg font-medium transition-colors;
	}
	.badge {
		@apply px-2 py-0.5 rounded-full text-xs font-medium;
	}
	.badge-green {
		@apply bg-green-900/40 text-green-400;
	}
	.badge-gray {
		@apply bg-slate-700 text-slate-400;
	}
</style>
