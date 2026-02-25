<script lang="ts">
	import { enhance } from '$app/forms';
	import StatCard from '$lib/components/StatCard.svelte';

	interface Stats {
		total_subscribers: number;
		active_subscribers: number;
		mrr_cents: number;
		arr_cents: number;
		new_subscribers_7d: number;
		churn_7d: number;
	}

	interface PromoCode {
		id: string;
		code: string;
		discount_type: 'percent' | 'fixed';
		discount_value: number;
		max_uses: number | null;
		used_count: number;
		expires_at: string | null;
		is_active: boolean;
	}

	interface Props {
		data: { stats: Stats | null; promoCodes: PromoCode[] };
		form: { promoError?: string; promoSuccess?: boolean } | null;
	}

	let { data, form }: Props = $props();

	const s = $derived(data.stats);
	let showNewPromoForm = $state(false);

	function formatMoney(cents: number): string {
		return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(cents / 100);
	}

	function formatDate(d: string): string {
		return new Date(d).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
	}
</script>

<svelte:head>
	<title>Billing — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<div class="mb-6">
		<h1 class="text-2xl font-bold text-slate-100">Billing</h1>
		<p class="text-slate-400 text-sm mt-1">Revenue dashboard and promo code management</p>
	</div>

	<!-- Revenue KPIs -->
	<div class="grid grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
		<StatCard label="MRR" value={s ? formatMoney(s.mrr_cents) : '—'} icon="💰" loading={!s} />
		<StatCard label="ARR" value={s ? formatMoney(s.arr_cents) : '—'} icon="📈" loading={!s} />
		<StatCard
			label="Active Subscribers"
			value={s?.active_subscribers ?? '—'}
			icon="✅"
			loading={!s}
		/>
		<StatCard
			label="Net Growth (7d)"
			value={s ? `+${s.new_subscribers_7d - s.churn_7d}` : '—'}
			icon="📊"
			loading={!s}
		/>
	</div>

	<!-- 7-day summary -->
	{#if s}
		<div class="card mb-8">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider mb-4">
				Last 7 Days
			</h2>
			<div class="grid grid-cols-3 gap-6">
				<div>
					<p class="text-sm text-slate-400">New subscribers</p>
					<p class="text-2xl font-bold text-green-400 mt-1">+{s.new_subscribers_7d}</p>
				</div>
				<div>
					<p class="text-sm text-slate-400">Churned</p>
					<p class="text-2xl font-bold {s.churn_7d > 0 ? 'text-red-400' : 'text-slate-400'} mt-1">
						{s.churn_7d > 0 ? `-${s.churn_7d}` : '0'}
					</p>
				</div>
				<div>
					<p class="text-sm text-slate-400">Revenue per subscriber</p>
					<p class="text-2xl font-bold text-slate-100 mt-1">
						{s.active_subscribers > 0 ? formatMoney(Math.round(s.mrr_cents / s.active_subscribers)) : '—'}
					</p>
				</div>
			</div>
		</div>
	{/if}

	<!-- Promo Codes -->
	<div class="card">
		<div class="flex items-center justify-between mb-4">
			<h2 class="text-sm font-semibold text-slate-400 uppercase tracking-wider">Promo Codes</h2>
			<button class="btn-primary btn-sm" onclick={() => (showNewPromoForm = !showNewPromoForm)}>
				{showNewPromoForm ? 'Cancel' : '+ New Code'}
			</button>
		</div>

		{#if form?.promoError}
			<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-4">
				{form.promoError}
			</div>
		{/if}
		{#if form?.promoSuccess}
			<div class="bg-green-500/10 border border-green-500/30 text-green-400 text-sm px-4 py-3 rounded-lg mb-4">
				Promo code saved successfully.
			</div>
		{/if}

		{#if showNewPromoForm}
			<form method="POST" action="?/createPromo" use:enhance class="bg-slate-700/30 rounded-lg p-4 mb-4">
				<div class="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-3">
					<div>
						<label class="label" for="code">Code</label>
						<input id="code" name="code" type="text" class="input" placeholder="WELCOME20" required />
					</div>
					<div>
						<label class="label" for="discount_type">Type</label>
						<select id="discount_type" name="discount_type" class="select" required>
							<option value="percent">Percent off</option>
							<option value="fixed">Fixed amount</option>
						</select>
					</div>
					<div>
						<label class="label" for="discount_value">Value</label>
						<input id="discount_value" name="discount_value" type="number" class="input" placeholder="20" min="0" step="0.01" required />
					</div>
					<div>
						<label class="label" for="max_uses">Max Uses</label>
						<input id="max_uses" name="max_uses" type="number" class="input" placeholder="Unlimited" min="1" />
					</div>
				</div>
				<div class="flex items-center gap-4">
					<div>
						<label class="label" for="expires_at">Expires</label>
						<input id="expires_at" name="expires_at" type="date" class="input w-40" />
					</div>
					<div class="flex items-center gap-2 mt-5">
						<input id="is_active_promo" name="is_active" type="checkbox" checked class="w-4 h-4" />
						<label for="is_active_promo" class="text-sm text-slate-300">Active</label>
					</div>
					<button type="submit" class="btn-primary mt-5">Create</button>
				</div>
			</form>
		{/if}

		<!-- Promo table -->
		{#if data.promoCodes.length === 0}
			<p class="text-slate-500 text-sm py-4">No promo codes yet.</p>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-left">
					<thead>
						<tr class="border-b border-slate-700">
							<th class="table-header">Code</th>
							<th class="table-header">Discount</th>
							<th class="table-header">Used</th>
							<th class="table-header">Expires</th>
							<th class="table-header">Status</th>
							<th class="table-header"></th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-700/50">
						{#each data.promoCodes as promo}
							<tr class="table-row">
								<td class="table-cell">
									<code class="text-sm font-bold text-slate-100">{promo.code}</code>
								</td>
								<td class="table-cell">
									{#if promo.discount_type === 'percent'}
										{promo.discount_value}% off
									{:else}
										${promo.discount_value} off
									{/if}
								</td>
								<td class="table-cell text-slate-300">
									{promo.used_count}{promo.max_uses ? ` / ${promo.max_uses}` : ' / ∞'}
								</td>
								<td class="table-cell text-slate-400">
									{promo.expires_at ? formatDate(promo.expires_at) : '—'}
								</td>
								<td class="table-cell">
									{#if promo.is_active}
										<span class="badge-active">Active</span>
									{:else}
										<span class="badge-suspended">Inactive</span>
									{/if}
								</td>
								<td class="table-cell">
									{#if promo.is_active}
										<form method="POST" action="?/deactivatePromo" use:enhance>
											<input type="hidden" name="id" value={promo.id} />
											<button type="submit" class="btn-danger btn-sm">Deactivate</button>
										</form>
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
