<!-- dashboard/+page.svelte — Reseller dashboard (P14-T05) -->
<script lang="ts">
	import type { PageData } from './$types';
	export let data: PageData;

	function formatCurrency(cents: number): string {
		return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(cents / 100);
	}
</script>

<svelte:head>
	<title>Dashboard — Roost Reseller</title>
</svelte:head>

<div class="space-y-6">
	<div>
		<h1 class="text-2xl font-bold text-white">Dashboard</h1>
		<p class="text-slate-400 mt-1">Welcome back, {data.dashboard?.reseller_name ?? 'Partner'}.</p>
	</div>

	<!-- Stats grid -->
	<div class="grid grid-cols-2 xl:grid-cols-4 gap-4">
		<div class="stat-card">
			<span class="stat-label">Total Subscribers</span>
			<span class="stat-value">{data.dashboard?.total_subscribers ?? 0}</span>
		</div>
		<div class="stat-card">
			<span class="stat-label">New This Month</span>
			<span class="stat-value text-green-400">{data.dashboard?.active_this_month ?? 0}</span>
		</div>
		<div class="stat-card">
			<span class="stat-label">MRR (Your Share)</span>
			<span class="stat-value">{formatCurrency(data.dashboard?.mrr_cents ?? 0)}</span>
		</div>
		<div class="stat-card">
			<span class="stat-label">Churn This Month</span>
			<span class="stat-value {(data.dashboard?.churn_this_month ?? 0) > 0 ? 'text-red-400' : 'text-white'}">{data.dashboard?.churn_this_month ?? 0}</span>
		</div>
	</div>

	<!-- Quick links -->
	<div class="grid grid-cols-1 md:grid-cols-2 gap-4">
		<a href="/subscribers" class="card flex items-center gap-4 hover:border-roost-500/50 transition-colors group">
			<div class="w-10 h-10 bg-slate-700 rounded-lg flex items-center justify-center text-slate-400 group-hover:text-roost-400 transition-colors">
				<svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z"></path></svg>
			</div>
			<div>
				<p class="text-white font-medium">Subscribers</p>
				<p class="text-sm text-slate-400">Manage and create subscriber accounts</p>
			</div>
		</a>
		<a href="/revenue" class="card flex items-center gap-4 hover:border-roost-500/50 transition-colors group">
			<div class="w-10 h-10 bg-slate-700 rounded-lg flex items-center justify-center text-slate-400 group-hover:text-roost-400 transition-colors">
				<svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8c-1.657 0-3 .895-3 2s1.343 2 3 2 3 .895 3 2-1.343 2-3 2m0-8c1.11 0 2.08.402 2.599 1M12 8V7m0 1v8m0 0v1m0-1c-1.11 0-2.08-.402-2.599-1M21 12a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>
			</div>
			<div>
				<p class="text-white font-medium">Revenue</p>
				<p class="text-sm text-slate-400">Monthly breakdown and earnings</p>
			</div>
		</a>
	</div>
</div>
