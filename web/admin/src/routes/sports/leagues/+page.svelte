<script lang="ts">
	import { enhance } from '$app/forms';

	interface League {
		id: string;
		name: string;
		abbreviation: string;
		sport: string;
		country_code: string | null;
		thesportsdb_id: string | null;
		is_active: boolean;
		sort_order: number;
	}

	interface Props {
		data: { leagues: League[] };
		form: { success: boolean; message: string } | null;
	}

	let { data, form }: Props = $props();
	let syncing = $state(false);
</script>

<svelte:head>
	<title>Sports Leagues — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<div class="mb-6 flex items-center justify-between">
		<div>
			<nav class="text-sm text-slate-500 mb-1">
				<a href="/sports" class="hover:text-slate-300">Sports</a>
				<span class="mx-1">/</span>
				<span>Leagues</span>
			</nav>
			<h1 class="text-2xl font-bold text-slate-100">Sports Leagues</h1>
		</div>
		<form method="POST" action="?/sync" use:enhance={() => {
			syncing = true;
			return async ({ update }) => {
				syncing = false;
				await update();
			};
		}}>
			<button type="submit" class="btn-primary" disabled={syncing}>
				{syncing ? 'Syncing...' : 'Sync All Schedules'}
			</button>
		</form>
	</div>

	{#if form}
		<div class="mb-4 p-3 rounded-lg {form.success ? 'bg-green-900/30 text-green-400' : 'bg-red-900/30 text-red-400'} text-sm">
			{form.message}
		</div>
	{/if}

	<div class="card">
		{#if data.leagues.length === 0}
			<p class="text-slate-400 text-sm py-8 text-center">No leagues configured.</p>
		{:else}
			<table class="w-full text-sm">
				<thead>
					<tr class="text-left text-slate-500 border-b border-slate-700">
						<th class="pb-3 font-medium">League</th>
						<th class="pb-3 font-medium">Abbr</th>
						<th class="pb-3 font-medium">Sport</th>
						<th class="pb-3 font-medium">Country</th>
						<th class="pb-3 font-medium">TheSportsDB</th>
						<th class="pb-3 font-medium">Status</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-700/50">
					{#each data.leagues as league}
						<tr class="hover:bg-slate-700/30 transition-colors">
							<td class="py-3 font-medium text-slate-100">{league.name}</td>
							<td class="py-3 text-slate-400 font-mono">{league.abbreviation}</td>
							<td class="py-3 text-slate-400 capitalize">{league.sport.replace('_', ' ')}</td>
							<td class="py-3 text-slate-400">{league.country_code ?? '—'}</td>
							<td class="py-3 text-slate-500 font-mono text-xs">{league.thesportsdb_id ?? '—'}</td>
							<td class="py-3">
								<span class="badge {league.is_active ? 'badge-green' : 'badge-gray'}">
									{league.is_active ? 'Active' : 'Inactive'}
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
	.btn-primary {
		@apply inline-flex items-center px-4 py-2 bg-roost-500 hover:bg-roost-600 disabled:opacity-60 text-white rounded-lg font-medium transition-colors;
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
