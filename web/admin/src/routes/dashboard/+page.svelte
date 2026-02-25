<script lang="ts">
	import StatCard from '$lib/components/StatCard.svelte';

	interface Stats {
		total_subscribers: number;
		active_subscribers: number;
		active_streams: number;
		total_channels: number;
		mrr_cents: number;
		arr_cents: number;
		new_subscribers_7d: number;
		churn_7d: number;
	}

	interface Props {
		data: { stats: Stats | null };
	}

	let { data }: Props = $props();

	const s = $derived(data.stats);

	function formatMoney(cents: number): string {
		return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(
			cents / 100
		);
	}
</script>

<svelte:head>
	<title>Dashboard — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<!-- Header -->
	<div class="mb-8">
		<h1 class="text-2xl font-bold text-slate-100">Dashboard</h1>
		<p class="text-slate-400 text-sm mt-1">Roost service overview</p>
	</div>

	<!-- KPI Grid -->
	<div class="grid grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
		<StatCard
			label="Total Subscribers"
			value={s?.total_subscribers ?? '—'}
			icon="👥"
			loading={!s}
		/>
		<StatCard
			label="Active Subscribers"
			value={s?.active_subscribers ?? '—'}
			icon="✅"
			loading={!s}
		/>
		<StatCard
			label="Live Streams"
			value={s?.active_streams ?? '—'}
			icon="🔴"
			loading={!s}
		/>
		<StatCard
			label="Channels"
			value={s?.total_channels ?? '—'}
			icon="📺"
			loading={!s}
		/>
	</div>

	<div class="grid grid-cols-1 lg:grid-cols-2 gap-4 mb-8">
		<StatCard
			label="Monthly Recurring Revenue"
			value={s ? formatMoney(s.mrr_cents) : '—'}
			change={s?.new_subscribers_7d ? Math.round((s.new_subscribers_7d / Math.max(s.total_subscribers, 1)) * 100) : undefined}
			changeLabel="subscriber growth (7d)"
			icon="💰"
			loading={!s}
		/>
		<StatCard
			label="Annual Recurring Revenue"
			value={s ? formatMoney(s.arr_cents) : '—'}
			icon="📈"
			loading={!s}
		/>
	</div>

	<!-- 7-day activity -->
	<div class="grid grid-cols-1 lg:grid-cols-2 gap-4 mb-8">
		<div class="card">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-4">
				Last 7 Days
			</h2>
			{#if !s}
				<div class="space-y-3">
					{#each { length: 3 } as _}
						<div class="h-4 bg-slate-700 rounded animate-pulse"></div>
					{/each}
				</div>
			{:else}
				<div class="space-y-3">
					<div class="flex justify-between items-center">
						<span class="text-sm text-slate-400">New subscribers</span>
						<span class="text-sm font-semibold text-green-400">+{s.new_subscribers_7d}</span>
					</div>
					<div class="flex justify-between items-center">
						<span class="text-sm text-slate-400">Churned</span>
						<span class="text-sm font-semibold {s.churn_7d > 0 ? 'text-red-400' : 'text-slate-400'}">
							{s.churn_7d > 0 ? `-${s.churn_7d}` : '0'}
						</span>
					</div>
					<div class="flex justify-between items-center">
						<span class="text-sm text-slate-400">Net growth</span>
						<span class="text-sm font-semibold {s.new_subscribers_7d - s.churn_7d >= 0 ? 'text-green-400' : 'text-red-400'}">
							{s.new_subscribers_7d - s.churn_7d >= 0 ? '+' : ''}{s.new_subscribers_7d - s.churn_7d}
						</span>
					</div>
				</div>
			{/if}
		</div>

		<div class="card">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-4">
				Quick Actions
			</h2>
			<div class="space-y-2">
				<a href="/channels/new" class="btn-primary btn-sm block text-center w-full">
					+ Add Channel
				</a>
				<a href="/subscribers" class="btn-secondary btn-sm block text-center w-full">
					View Subscribers
				</a>
				<a href="/streams" class="btn-secondary btn-sm block text-center w-full">
					Monitor Streams
				</a>
				<a href="/system" class="btn-secondary btn-sm block text-center w-full">
					System Health
				</a>
			</div>
		</div>
	</div>

	{#if !data.stats}
		<div class="bg-yellow-500/10 border border-yellow-500/30 text-yellow-400 text-sm px-4 py-3 rounded-lg">
			Could not connect to Roost API. Dashboard stats unavailable. Check that the backend services are running.
		</div>
	{/if}
</div>
