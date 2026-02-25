<script lang="ts">
	import type { PageData } from './$types';
	import PlanBadge from '$lib/components/PlanBadge.svelte';
	import TokenCard from '$lib/components/TokenCard.svelte';
	import AddToOwlCard from '$lib/components/AddToOwlCard.svelte';
	import type { ApiToken } from '$lib/api';

	export let data: PageData;

	let apiToken = data.apiToken;

	async function regenerateToken() {
		const res = await fetch('/api/token/regenerate', { method: 'POST' });
		if (res.ok) {
			apiToken = await res.json() as ApiToken;
		}
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-US', { year: 'numeric', month: 'long', day: 'numeric' });
	}
</script>

<svelte:head>
	<title>Dashboard — Roost</title>
</svelte:head>

<div class="max-w-4xl mx-auto px-4 py-10">
	<!-- Header -->
	<div class="flex items-start justify-between mb-8">
		<div>
			<h1 class="text-2xl font-bold text-white">Dashboard</h1>
			<p class="text-slate-400 text-sm mt-1">Welcome back, {data.subscriber.name || data.subscriber.email}</p>
		</div>
		<div class="flex items-center gap-3">
			<PlanBadge
				status={data.subscription?.status ?? null}
				plan={data.subscription?.plan ?? null}
				isFounder={data.subscriber.is_founder}
			/>
		</div>
	</div>

	{#if !data.subscription && !data.subscriber.is_founder}
		<!-- No subscription CTA -->
		<div class="card border-dashed border-roost-500/50 text-center py-12 mb-8">
			<h2 class="text-xl font-semibold text-white mb-3">No active subscription</h2>
			<p class="text-slate-400 mb-6">Subscribe to get your API token and access live channels.</p>
			<a href="/plans" class="btn-primary px-8 py-3">Browse Plans</a>
		</div>
	{:else}
		<!-- Subscription card -->
		<div class="card mb-6">
			<div class="flex items-center justify-between mb-3">
				<h2 class="text-base font-semibold text-slate-100">Subscription</h2>
				<a href="/billing" class="text-sm text-roost-400 hover:text-roost-300">Manage →</a>
			</div>
			{#if data.subscriber.is_founder}
				<p class="text-slate-300 text-sm">Founding Family — lifetime access, no billing.</p>
			{:else if data.subscription}
				<div class="grid grid-cols-2 gap-4 text-sm">
					<div>
						<span class="text-slate-500">Plan</span>
						<p class="text-white capitalize mt-0.5">{data.subscription.plan}</p>
					</div>
					<div>
						<span class="text-slate-500">Renews</span>
						<p class="text-white mt-0.5">
							{data.subscription.cancel_at_period_end ? 'Cancels' : 'Renews'}
							{formatDate(data.subscription.current_period_end)}
						</p>
					</div>
					<div>
						<span class="text-slate-500">Billing</span>
						<p class="text-white capitalize mt-0.5">{data.subscription.billing_period}</p>
					</div>
					<div>
						<span class="text-slate-500">Streams</span>
						<p class="text-white mt-0.5">
							Up to {data.subscription.plan === 'basic' ? 2 : data.subscription.plan === 'premium' ? 4 : 6} concurrent
						</p>
					</div>
				</div>
			{/if}
		</div>
	{/if}

	<!-- API Token -->
	<div class="mb-6">
		<TokenCard token={apiToken} onRegenerate={regenerateToken} />
	</div>

	<!-- Add to Owl -->
	{#if apiToken}
		<AddToOwlCard token={apiToken.token} />
	{/if}

	<!-- Quick links -->
	<div class="mt-6 grid grid-cols-1 sm:grid-cols-2 gap-4">
		<a
			href="/dashboard/profiles"
			class="card flex items-center justify-between hover:border-roost-500/50 transition-colors group"
		>
			<div>
				<p class="text-white font-medium">Profiles</p>
				<p class="text-slate-400 text-sm mt-0.5">Manage who watches Roost</p>
			</div>
			<span class="text-slate-500 group-hover:text-roost-400 transition-colors text-lg">→</span>
		</a>
		<a
			href="/billing"
			class="card flex items-center justify-between hover:border-roost-500/50 transition-colors group"
		>
			<div>
				<p class="text-white font-medium">Billing</p>
				<p class="text-slate-400 text-sm mt-0.5">Invoices, subscription, refunds</p>
			</div>
			<span class="text-slate-500 group-hover:text-roost-400 transition-colors text-lg">→</span>
		</a>
	</div>
</div>
