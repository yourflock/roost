<script lang="ts">
	import { enhance } from '$app/forms';
	import ConfirmModal from '$lib/components/ConfirmModal.svelte';

	interface Subscriber {
		id: string;
		email: string;
		name: string;
		created_at: string;
		is_founder: boolean;
		stream_count: number;
		subscription: {
			id: string;
			status: string;
			plan: string;
			billing_period: string;
			current_period_end: string;
			cancel_at_period_end: boolean;
			stripe_subscription_id: string;
			stripe_customer_id: string;
		} | null;
	}

	interface Invoice {
		id: string;
		amount: number;
		currency: string;
		status: string;
		created_at: string;
		period_start: string;
		period_end: string;
		pdf_url: string | null;
	}

	interface ApiToken {
		token: string;
		created_at: string;
		last_used_at: string | null;
	}

	interface Props {
		data: { subscriber: Subscriber; invoices: Invoice[]; tokens: ApiToken[] };
		form: { error?: string; success?: boolean; action?: string } | null;
	}

	let { data, form }: Props = $props();

	const sub = data.subscriber;
	let showSuspendModal = $state(false);
	let showReinstateModal = $state(false);
	let suspendLoading = $state(false);
	let reinstateLoading = $state(false);
	let suspendReason = $state('');

	function formatDate(d: string): string {
		return new Date(d).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
	}

	function formatMoney(cents: number, currency: string): string {
		return new Intl.NumberFormat('en-US', { style: 'currency', currency: currency.toUpperCase() }).format(cents / 100);
	}
</script>

<svelte:head>
	<title>{sub.name} — Subscribers — Roost Admin</title>
</svelte:head>

<ConfirmModal
	open={showSuspendModal}
	title="Suspend Subscriber"
	message="Suspend {sub.name} ({sub.email})? They will lose access to all streams immediately."
	confirmLabel="Suspend"
	danger
	loading={suspendLoading}
	onconfirm={() => {
		suspendLoading = true;
		document.getElementById('suspend-form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
	}}
	oncancel={() => (showSuspendModal = false)}
/>

<ConfirmModal
	open={showReinstateModal}
	title="Reinstate Subscriber"
	message="Reinstate {sub.name}? Their subscription will be reactivated."
	confirmLabel="Reinstate"
	loading={reinstateLoading}
	onconfirm={() => {
		reinstateLoading = true;
		document.getElementById('reinstate-form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
	}}
	oncancel={() => (showReinstateModal = false)}
/>

<!-- Hidden forms -->
<form id="suspend-form" method="POST" action="?/suspend" use:enhance={() => {
	return async ({ update }) => { suspendLoading = false; showSuspendModal = false; update(); };
}}>
	<input type="hidden" name="reason" value={suspendReason || 'Suspended by admin'} />
</form>
<form id="reinstate-form" method="POST" action="?/reinstate" use:enhance={() => {
	return async ({ update }) => { reinstateLoading = false; showReinstateModal = false; update(); };
}}>
</form>

<div class="p-6 max-w-5xl mx-auto">
	<div class="mb-6">
		<a href="/subscribers" class="text-sm text-slate-400 hover:text-slate-200 transition-colors">
			← Back to Subscribers
		</a>
	</div>

	{#if form?.error}
		<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-4">
			{form.error}
		</div>
	{/if}
	{#if form?.success}
		<div class="bg-green-500/10 border border-green-500/30 text-green-400 text-sm px-4 py-3 rounded-lg mb-4">
			Subscriber {form.action} successfully.
		</div>
	{/if}

	<!-- Header -->
	<div class="flex items-start justify-between mb-6">
		<div>
			<div class="flex items-center gap-3 mb-1">
				<h1 class="text-2xl font-bold text-slate-100">{sub.name}</h1>
				{#if sub.is_founder}
					<span class="badge-founder">Founder</span>
				{/if}
				{#if sub.subscription?.status === 'active'}
					<span class="badge-active">Active</span>
				{:else if sub.subscription?.status === 'suspended'}
					<span class="badge-suspended">Suspended</span>
				{:else if sub.subscription?.status === 'cancelled'}
					<span class="badge-cancelled">Cancelled</span>
				{/if}
			</div>
			<p class="text-slate-400">{sub.email}</p>
			<p class="text-sm text-slate-500 mt-1">Joined {formatDate(sub.created_at)}</p>
		</div>
		<div class="flex gap-2">
			{#if sub.subscription?.status !== 'suspended'}
				<button class="btn-danger btn-sm" onclick={() => (showSuspendModal = true)}>
					Suspend
				</button>
			{:else}
				<button class="btn-secondary btn-sm" onclick={() => (showReinstateModal = true)}>
					Reinstate
				</button>
			{/if}
		</div>
	</div>

	<div class="grid grid-cols-1 lg:grid-cols-3 gap-6 mb-6">
		<!-- Subscription -->
		<div class="card lg:col-span-2">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-4">Subscription</h2>
			{#if sub.subscription}
				<div class="space-y-3">
					<div class="flex justify-between">
						<span class="text-sm text-slate-400">Plan</span>
						<span class="text-sm text-slate-200 capitalize">{sub.subscription.plan} ({sub.subscription.billing_period})</span>
					</div>
					<div class="flex justify-between">
						<span class="text-sm text-slate-400">Status</span>
						<span class="text-sm text-slate-200 capitalize">{sub.subscription.status}</span>
					</div>
					<div class="flex justify-between">
						<span class="text-sm text-slate-400">Renews</span>
						<span class="text-sm text-slate-200">{formatDate(sub.subscription.current_period_end)}</span>
					</div>
					{#if sub.subscription.cancel_at_period_end}
						<div class="text-xs text-yellow-400 bg-yellow-500/10 border border-yellow-500/30 rounded px-3 py-2">
							Cancels at end of billing period
						</div>
					{/if}
					<div class="flex justify-between">
						<span class="text-sm text-slate-400">Stripe Customer</span>
						<code class="text-xs text-slate-400">{sub.subscription.stripe_customer_id}</code>
					</div>
				</div>
			{:else}
				<p class="text-slate-500 text-sm">No active subscription.</p>
			{/if}
		</div>

		<!-- Stats -->
		<div class="card">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-4">Stats</h2>
			<div class="space-y-3">
				<div class="flex justify-between">
					<span class="text-sm text-slate-400">Active Streams</span>
					<span class="text-sm font-semibold {sub.stream_count > 0 ? 'text-green-400' : 'text-slate-200'}">{sub.stream_count}</span>
				</div>
				<div class="flex justify-between">
					<span class="text-sm text-slate-400">API Tokens</span>
					<span class="text-sm text-slate-200">{data.tokens.length}</span>
				</div>
				<div class="flex justify-between">
					<span class="text-sm text-slate-400">Invoices</span>
					<span class="text-sm text-slate-200">{data.invoices.length}</span>
				</div>
			</div>
		</div>
	</div>

	<!-- API Tokens -->
	<div class="card mb-6">
		<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-4">API Tokens</h2>
		{#if data.tokens.length === 0}
			<p class="text-slate-500 text-sm">No API tokens issued.</p>
		{:else}
			<div class="space-y-2">
				{#each data.tokens as tok}
					<div class="flex items-center justify-between bg-slate-700/30 rounded-lg px-4 py-3">
						<code class="text-xs text-slate-300 font-mono">{tok.token.slice(0, 8)}...{tok.token.slice(-8)}</code>
						<div class="text-xs text-slate-500">
							Created {formatDate(tok.created_at)}
							{#if tok.last_used_at}
								· Last used {formatDate(tok.last_used_at)}
							{/if}
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>

	<!-- Invoices -->
	<div class="card">
		<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-4">Invoice History</h2>
		{#if data.invoices.length === 0}
			<p class="text-slate-500 text-sm">No invoices found.</p>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-left">
					<thead>
						<tr class="border-b border-slate-700">
							<th class="table-header">Date</th>
							<th class="table-header">Period</th>
							<th class="table-header">Amount</th>
							<th class="table-header">Status</th>
							<th class="table-header"></th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-700/50">
						{#each data.invoices as inv}
							<tr class="table-row">
								<td class="table-cell">{formatDate(inv.created_at)}</td>
								<td class="table-cell text-xs text-slate-400">
									{formatDate(inv.period_start)} – {formatDate(inv.period_end)}
								</td>
								<td class="table-cell font-medium">{formatMoney(inv.amount, inv.currency)}</td>
								<td class="table-cell">
									{#if inv.status === 'paid'}
										<span class="badge-active">Paid</span>
									{:else if inv.status === 'open'}
										<span class="badge-degraded">Open</span>
									{:else if inv.status === 'void'}
										<span class="text-slate-500 text-xs">Void</span>
									{:else}
										<span class="badge-suspended">Uncollectible</span>
									{/if}
								</td>
								<td class="table-cell">
									{#if inv.pdf_url}
										<a href={inv.pdf_url} target="_blank" rel="noopener" class="text-roost-400 text-xs hover:underline">
											PDF
										</a>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</div>
</div>
