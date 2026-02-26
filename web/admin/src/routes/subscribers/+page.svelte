<script lang="ts">
	import { goto } from '$app/navigation';

	interface Subscriber {
		id: string;
		email: string;
		name: string;
		created_at: string;
		is_founder: boolean;
		stream_count: number;
		subscription: {
			status: string;
			plan: string;
			billing_period: string;
			current_period_end: string;
		} | null;
	}

	interface Props {
		data: {
			subscribers: Subscriber[];
			total: number;
			page: number;
			per_page: number;
			search: string;
			plan: string;
			status: string;
		};
	}

	let { data }: Props = $props();

	let search = $derived(data.search);
	let plan = $derived(data.plan);
	let status = $derived(data.status);

	function applyFilters() {
		const params = new URLSearchParams();
		if (search) params.set('search', search);
		if (plan) params.set('plan', plan);
		if (status) params.set('status', status);
		goto(`/subscribers?${params.toString()}`);
	}

	function formatDate(d: string): string {
		return new Date(d).toLocaleDateString('en-US', {
			month: 'short',
			day: 'numeric',
			year: 'numeric'
		});
	}

	const totalPages = $derived(Math.ceil(data.total / data.per_page));
</script>

<svelte:head>
	<title>Subscribers — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<div class="mb-6">
		<h1 class="text-2xl font-bold text-slate-100">Subscribers</h1>
		<p class="text-slate-400 text-sm mt-1">{data.total} total subscribers</p>
	</div>

	<!-- Filters -->
	<div class="flex gap-3 mb-5 flex-wrap">
		<input
			type="search"
			class="input max-w-xs"
			placeholder="Search by name or email..."
			bind:value={search}
			onkeydown={(e) => e.key === 'Enter' && applyFilters()}
		/>
		<select class="select w-36" bind:value={plan} onchange={applyFilters}>
			<option value="">All plans</option>
			<option value="basic">Basic</option>
			<option value="premium">Premium</option>
			<option value="family">Family</option>
		</select>
		<select class="select w-36" bind:value={status} onchange={applyFilters}>
			<option value="">All statuses</option>
			<option value="active">Active</option>
			<option value="cancelled">Cancelled</option>
			<option value="suspended">Suspended</option>
			<option value="trialing">Trialing</option>
			<option value="past_due">Past Due</option>
		</select>
		<button class="btn-primary" onclick={applyFilters}>Search</button>
	</div>

	<!-- Table -->
	<div class="overflow-x-auto rounded-xl border border-slate-700">
		<table class="w-full text-left">
			<thead class="bg-slate-800/80 border-b border-slate-700">
				<tr>
					<th class="table-header">Subscriber</th>
					<th class="table-header">Plan</th>
					<th class="table-header">Status</th>
					<th class="table-header">Streams</th>
					<th class="table-header">Joined</th>
					<th class="table-header">Renews</th>
					<th class="table-header"></th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-700/50">
				{#if data.subscribers.length === 0}
					<tr>
						<td colspan="7" class="table-cell text-center text-slate-500 py-10">
							No subscribers found.
						</td>
					</tr>
				{:else}
					{#each data.subscribers as sub}
						<tr class="table-row">
							<td class="table-cell">
								<div>
									<div class="flex items-center gap-2">
										<span class="font-medium text-slate-100">{sub.name}</span>
										{#if sub.is_founder}
											<span class="badge-founder">Founder</span>
										{/if}
									</div>
									<div class="text-xs text-slate-400">{sub.email}</div>
								</div>
							</td>
							<td class="table-cell">
								<span class="text-slate-300 capitalize">{sub.subscription?.plan ?? '—'}</span>
								{#if sub.subscription?.billing_period}
									<span class="text-xs text-slate-500 ml-1"
										>({sub.subscription.billing_period})</span
									>
								{/if}
							</td>
							<td class="table-cell">
								{#if !sub.subscription}
									<span class="text-slate-500 text-sm">No plan</span>
								{:else if sub.subscription.status === 'active'}
									<span class="badge-active">Active</span>
								{:else if sub.subscription.status === 'cancelled'}
									<span class="badge-cancelled">Cancelled</span>
								{:else if sub.subscription.status === 'suspended'}
									<span class="badge-suspended">Suspended</span>
								{:else if sub.subscription.status === 'trialing'}
									<span class="badge-staff">Trialing</span>
								{:else if sub.subscription.status === 'past_due'}
									<span class="badge-suspended">Past Due</span>
								{/if}
							</td>
							<td class="table-cell">
								<span class="text-slate-300">{sub.stream_count}</span>
							</td>
							<td class="table-cell text-slate-400">{formatDate(sub.created_at)}</td>
							<td class="table-cell text-slate-400">
								{sub.subscription?.current_period_end
									? formatDate(sub.subscription.current_period_end)
									: '—'}
							</td>
							<td class="table-cell">
								<a href="/subscribers/{sub.id}" class="btn-secondary btn-sm">View</a>
							</td>
						</tr>
					{/each}
				{/if}
			</tbody>
		</table>
	</div>

	<!-- Pagination -->
	{#if totalPages > 1}
		<div class="flex items-center justify-between mt-4">
			<p class="text-sm text-slate-400">
				Page {data.page} of {totalPages} ({data.total} results)
			</p>
			<div class="flex gap-2">
				{#if data.page > 1}
					<a
						href="/subscribers?page={data.page -
							1}&search={data.search}&plan={data.plan}&status={data.status}"
						class="btn-secondary btn-sm"
					>
						Previous
					</a>
				{/if}
				{#if data.page < totalPages}
					<a
						href="/subscribers?page={data.page +
							1}&search={data.search}&plan={data.plan}&status={data.status}"
						class="btn-secondary btn-sm"
					>
						Next
					</a>
				{/if}
			</div>
		</div>
	{/if}
</div>
