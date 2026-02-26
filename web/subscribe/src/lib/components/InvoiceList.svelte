<script lang="ts">
	import type { Invoice } from '$lib/api';

	export let invoices: Invoice[] = [];

	function formatAmount(amount: number, currency: string): string {
		return new Intl.NumberFormat('en-US', {
			style: 'currency',
			currency: currency.toUpperCase()
		}).format(amount / 100);
	}

	function formatDate(dateStr: string): string {
		return new Date(dateStr).toLocaleDateString('en-US', {
			year: 'numeric',
			month: 'short',
			day: 'numeric'
		});
	}

	function statusClass(status: string): string {
		switch (status) {
			case 'paid':
				return 'text-green-400';
			case 'open':
				return 'text-yellow-400';
			case 'void':
				return 'text-slate-400';
			default:
				return 'text-red-400';
		}
	}
</script>

{#if invoices.length === 0}
	<p class="text-slate-400 text-sm text-center py-8">No invoices yet.</p>
{:else}
	<div class="overflow-x-auto">
		<table class="w-full text-sm">
			<thead>
				<tr class="text-left text-slate-400 border-b border-slate-700">
					<th class="pb-3 pr-4 font-medium">Date</th>
					<th class="pb-3 pr-4 font-medium">Period</th>
					<th class="pb-3 pr-4 font-medium">Amount</th>
					<th class="pb-3 pr-4 font-medium">Status</th>
					<th class="pb-3 font-medium">Receipt</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-700/50">
				{#each invoices as invoice}
					<tr class="text-slate-300">
						<td class="py-3 pr-4">{formatDate(invoice.created_at)}</td>
						<td class="py-3 pr-4 text-slate-400">
							{formatDate(invoice.period_start)} – {formatDate(invoice.period_end)}
						</td>
						<td class="py-3 pr-4 font-medium">{formatAmount(invoice.amount, invoice.currency)}</td>
						<td class="py-3 pr-4">
							<span class="{statusClass(invoice.status)} capitalize">{invoice.status}</span>
						</td>
						<td class="py-3">
							{#if invoice.pdf_url}
								<a
									href={invoice.pdf_url}
									target="_blank"
									rel="noopener noreferrer"
									class="text-roost-400 hover:text-roost-300 underline text-xs"
								>
									PDF
								</a>
							{:else}
								<span class="text-slate-500 text-xs">—</span>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}
