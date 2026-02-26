<script lang="ts">
	import type { PageData } from './$types';
	import PlanBadge from '$lib/components/PlanBadge.svelte';
	import InvoiceList from '$lib/components/InvoiceList.svelte';

	export let data: PageData;

	let loading = false;
	let error = '';
	let successMessage = '';
	let confirmCancel = false;

	async function openBillingPortal() {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/billing/portal', { method: 'POST' });
			const body = await res.json();
			if (!res.ok) throw new Error(body.message ?? 'Failed to open billing portal.');
			window.location.href = body.url;
		} catch (e: unknown) {
			error = (e as Error).message ?? 'Service unavailable.';
		} finally {
			loading = false;
		}
	}

	async function cancelSubscription() {
		if (!confirmCancel) {
			confirmCancel = true;
			setTimeout(() => (confirmCancel = false), 6000);
			return;
		}
		loading = true;
		confirmCancel = false;
		error = '';
		try {
			const res = await fetch('/api/billing/cancel', { method: 'POST' });
			if (!res.ok) {
				const body = await res.json();
				throw new Error(body.message ?? 'Cancellation failed.');
			}
			successMessage =
				"Subscription cancelled. You'll retain access until the end of your billing period.";
			window.location.reload();
		} catch (e: unknown) {
			error = (e as Error).message ?? 'Service unavailable.';
		} finally {
			loading = false;
		}
	}

	async function resumeSubscription() {
		loading = true;
		error = '';
		try {
			const res = await fetch('/api/billing/resume', { method: 'POST' });
			if (!res.ok) {
				const body = await res.json();
				throw new Error(body.message ?? 'Failed to resume subscription.');
			}
			successMessage = 'Subscription resumed.';
			window.location.reload();
		} catch (e: unknown) {
			error = (e as Error).message ?? 'Service unavailable.';
		} finally {
			loading = false;
		}
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-US', {
			year: 'numeric',
			month: 'long',
			day: 'numeric'
		});
	}
</script>

<svelte:head>
	<title>Billing — Roost</title>
</svelte:head>

<div class="max-w-3xl mx-auto px-4 py-10">
	<div class="mb-8">
		<h1 class="text-2xl font-bold text-white">Billing</h1>
		<p class="text-slate-400 text-sm mt-1">Manage your subscription and view payment history.</p>
	</div>

	{#if error}
		<div
			class="bg-red-500/10 border border-red-500/30 rounded-lg px-4 py-3 text-red-400 text-sm mb-6"
		>
			{error}
		</div>
	{/if}

	{#if successMessage}
		<div
			class="bg-green-500/10 border border-green-500/30 rounded-lg px-4 py-3 text-green-400 text-sm mb-6"
		>
			{successMessage}
		</div>
	{/if}

	{#if data.subscriber.is_founder}
		<div class="card mb-6 border-purple-500/30">
			<div class="flex items-center gap-3">
				<span class="text-2xl">♾️</span>
				<div>
					<h2 class="font-semibold text-white">Founding Family</h2>
					<p class="text-slate-400 text-sm">
						Lifetime access — no billing, ever. Thank you for being here from the start.
					</p>
				</div>
			</div>
		</div>
	{:else if data.subscription}
		<!-- Current subscription -->
		<div class="card mb-6">
			<div class="flex items-center justify-between mb-4">
				<h2 class="font-semibold text-white">Current Subscription</h2>
				<PlanBadge status={data.subscription.status} plan={data.subscription.plan} />
			</div>

			<div class="grid grid-cols-2 gap-4 text-sm mb-6">
				<div>
					<span class="text-slate-500">Plan</span>
					<p class="text-white capitalize mt-0.5">{data.subscription.plan}</p>
				</div>
				<div>
					<span class="text-slate-500">Billing</span>
					<p class="text-white capitalize mt-0.5">{data.subscription.billing_period}</p>
				</div>
				<div>
					<span class="text-slate-500">
						{data.subscription.cancel_at_period_end ? 'Access until' : 'Next renewal'}
					</span>
					<p class="text-white mt-0.5">{formatDate(data.subscription.current_period_end)}</p>
				</div>
				<div>
					<span class="text-slate-500">Status</span>
					<p class="text-white capitalize mt-0.5">{data.subscription.status.replace('_', ' ')}</p>
				</div>
			</div>

			<div class="flex flex-wrap gap-3">
				<button on:click={openBillingPortal} class="btn-secondary text-sm" disabled={loading}>
					{loading ? 'Opening...' : 'Manage Payment & Plan'}
				</button>

				{#if data.subscription.cancel_at_period_end}
					<button on:click={resumeSubscription} class="btn-primary text-sm" disabled={loading}>
						Resume Subscription
					</button>
				{:else if data.subscription.status === 'active' || data.subscription.status === 'trialing'}
					<button
						on:click={cancelSubscription}
						class={confirmCancel
							? 'btn-danger'
							: 'text-red-400 hover:text-red-300 text-sm underline'}
						disabled={loading}
					>
						{confirmCancel ? 'Click again to confirm cancellation' : 'Cancel Subscription'}
					</button>
				{/if}
			</div>
		</div>
	{:else}
		<div class="card text-center py-10 mb-6">
			<p class="text-slate-400 mb-4">No active subscription.</p>
			<a href="/plans" class="btn-primary px-6">Browse Plans</a>
		</div>
	{/if}

	<!-- Invoice history -->
	<div class="card">
		<h2 class="font-semibold text-white mb-4">Invoice History</h2>
		<InvoiceList invoices={data.invoices} />
	</div>
</div>
