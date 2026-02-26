<script lang="ts">
	interface Column {
		key: string;
		label: string;
		sortable?: boolean;
		class?: string;
	}

	interface Props {
		columns: Column[];
		rows: Record<string, unknown>[];
		loading?: boolean;
		emptyMessage?: string;
	}

	let { columns, rows, loading = false, emptyMessage = 'No data found.' }: Props = $props();

	let sortKey = $state('');
	let sortDir = $state<'asc' | 'desc'>('asc');

	function toggleSort(key: string) {
		if (sortKey === key) {
			sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		} else {
			sortKey = key;
			sortDir = 'asc';
		}
	}

	const sortedRows = $derived(() => {
		if (!sortKey) return rows;
		return [...rows].sort((a, b) => {
			const av = a[sortKey];
			const bv = b[sortKey];
			const aStr = String(av ?? '');
			const bStr = String(bv ?? '');
			const cmp = aStr.localeCompare(bStr, undefined, { numeric: true });
			return sortDir === 'asc' ? cmp : -cmp;
		});
	});
</script>

<div class="overflow-x-auto rounded-xl border border-slate-700">
	<table class="w-full text-left">
		<thead class="bg-slate-800/80 border-b border-slate-700">
			<tr>
				{#each columns as col}
					<th class="table-header {col.class ?? ''}">
						{#if col.sortable}
							<button
								class="flex items-center gap-1 hover:text-slate-200 transition-colors"
								onclick={() => toggleSort(col.key)}
							>
								{col.label}
								{#if sortKey === col.key}
									<span class="text-roost-400">{sortDir === 'asc' ? '↑' : '↓'}</span>
								{:else}
									<span class="text-slate-600">↕</span>
								{/if}
							</button>
						{:else}
							{col.label}
						{/if}
					</th>
				{/each}
			</tr>
		</thead>
		<tbody class="divide-y divide-slate-700/50">
			{#if loading}
				{#each { length: 5 } as _item}
					<tr class="table-row">
						{#each columns as _colItem}
							<td class="table-cell">
								<div class="h-4 bg-slate-700 rounded animate-pulse w-24"></div>
							</td>
						{/each}
					</tr>
				{/each}
			{:else if sortedRows().length === 0}
				<tr>
					<td colspan={columns.length} class="table-cell text-center text-slate-500 py-10">
						{emptyMessage}
					</td>
				</tr>
			{:else}
				{#each sortedRows() as row}
					<tr class="table-row">
						{#each columns as col}
							<td class="table-cell {col.class ?? ''}">
								<slot name="cell" {row} key={col.key}>
									{String(row[col.key] ?? '')}
								</slot>
							</td>
						{/each}
					</tr>
				{/each}
			{/if}
		</tbody>
	</table>
</div>
