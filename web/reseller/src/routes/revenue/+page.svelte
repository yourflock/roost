<!-- revenue/+page.svelte — Revenue breakdown (P14-T05) -->
<script lang="ts">
	import type { PageData } from './$types';
	export let data: PageData;

	function formatCurrency(cents: number, currency = 'USD'): string {
		return new Intl.NumberFormat('en-US', { style: 'currency', currency: currency.toUpperCase() }).format(cents / 100);
	}

	const totalGross = data.revenue?.reduce((s, r) => s + r.gross_amount_cents, 0) ?? 0;
	const totalShare = data.revenue?.reduce((s, r) => s + r.reseller_share_cents, 0) ?? 0;
</script>

<svelte:head>
	<title>Revenue — Roost Reseller</title>
</svelte:head>

<div class="space-y-6">
	<div>
		<h1 class="text-2xl font-bold text-white">Revenue</h1>
		<p class="text-slate-400 mt-1">Monthly breakdown of your earnings from Roost.</p>
	</div>

	<!-- Totals -->
	<div class="grid grid-cols-2 gap-4">
		<div class="stat-card">
			<span class="stat-label">Total Gross (All Time)</span>
			<span class="stat-value">{formatCurrency(totalGross)}</span>
		</div>
		<div class="stat-card">
			<span class="stat-label">Your Share (All Time)</span>
			<span class="stat-value text-green-400">{formatCurrency(totalShare)}</span>
		</div>
	</div>

	<!-- Monthly table -->
	<div class="bg-slate-800 rounded-xl border border-slate-700 overflow-hidden">
		<div class="px-4 py-3 border-b border-slate-700">
			<h2 class="text-sm font-semibold text-slate-300">Monthly Breakdown</h2>
		</div>
		<table class="w-full text-sm">
			<thead>
				<tr class="border-b border-slate-700">
					<th class="px-4 py-3 text-left text-xs font-semibold text-slate-400 uppercase tracking-wider">Month</th>
					<th class="px-4 py-3 text-right text-xs font-semibold text-slate-400 uppercase tracking-wider">Subscribers</th>
					<th class="px-4 py-3 text-right text-xs font-semibold text-slate-400 uppercase tracking-wider">Gross Revenue</th>
					<th class="px-4 py-3 text-right text-xs font-semibold text-slate-400 uppercase tracking-wider">Your Share</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-700">
				{#each (data.revenue ?? []) as period}
					<tr class="hover:bg-slate-700/30 transition-colors">
						<td class="px-4 py-3 text-slate-200 font-mono">{period.month}</td>
						<td class="px-4 py-3 text-right text-slate-400">{period.subscriber_count}</td>
						<td class="px-4 py-3 text-right text-slate-300">{formatCurrency(period.gross_amount_cents, period.currency)}</td>
						<td class="px-4 py-3 text-right text-green-400 font-medium">{formatCurrency(period.reseller_share_cents, period.currency)}</td>
					</tr>
				{/each}
				{#if !data.revenue || data.revenue.length === 0}
					<tr>
						<td colspan="4" class="px-4 py-8 text-center text-slate-500">No revenue data yet.</td>
					</tr>
				{/if}
			</tbody>
		</table>
	</div>
</div>
