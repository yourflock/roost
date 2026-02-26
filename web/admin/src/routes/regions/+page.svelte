<!-- regions/+page.svelte — Region management (P14-T02) -->
<!-- Lists regions, country assignments, and subscriber count per region. -->
<script lang="ts">
	import type { PageData } from './$types';
	export let data: PageData;
</script>

<svelte:head>
	<title>Regions — Roost Admin</title>
</svelte:head>

<div class="space-y-6">
	<div class="flex items-center justify-between">
		<div>
			<h1 class="text-2xl font-bold text-white">Regions</h1>
			<p class="text-slate-400 mt-1">
				Manage regional content packages and subscriber assignments.
			</p>
		</div>
	</div>

	<!-- Region cards -->
	<div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
		{#each data.regions as region}
			<div class="bg-slate-800 rounded-lg border border-slate-700 p-5">
				<div class="flex items-start justify-between mb-3">
					<div>
						<span
							class="inline-block bg-slate-700 text-slate-300 text-xs font-mono px-2 py-0.5 rounded mb-1"
						>
							{region.code.toUpperCase()}
						</span>
						<h2 class="text-white font-semibold">{region.name}</h2>
					</div>
					<span
						class="inline-flex items-center px-2 py-1 rounded-full text-xs font-medium {region.is_active
							? 'bg-green-900/50 text-green-400'
							: 'bg-red-900/50 text-red-400'}"
					>
						{region.is_active ? 'Active' : 'Inactive'}
					</span>
				</div>

				<div class="space-y-2 text-sm">
					<div class="flex justify-between text-slate-400">
						<span>Subscribers</span>
						<span class="text-white font-medium">{region.subscriber_count ?? 0}</span>
					</div>
					<div class="flex justify-between text-slate-400">
						<span>Channels</span>
						<span class="text-white font-medium">{region.channel_count ?? 0}</span>
					</div>
				</div>

				<!-- Countries list -->
				{#if region.countries && region.countries.length > 0}
					<div class="mt-3 pt-3 border-t border-slate-700">
						<p class="text-xs text-slate-500 mb-2">Countries</p>
						<div class="flex flex-wrap gap-1">
							{#each region.countries.slice(0, 8) as country}
								<span class="bg-slate-700 text-slate-300 text-xs px-1.5 py-0.5 rounded font-mono"
									>{country}</span
								>
							{/each}
							{#if region.countries.length > 8}
								<span class="text-slate-500 text-xs px-1.5 py-0.5"
									>+{region.countries.length - 8} more</span
								>
							{/if}
						</div>
					</div>
				{/if}
			</div>
		{/each}
	</div>

	{#if data.regions.length === 0}
		<div class="bg-slate-800 rounded-lg border border-slate-700 p-12 text-center">
			<p class="text-slate-400">No regions found. Run migration 022 to seed default regions.</p>
		</div>
	{/if}
</div>
