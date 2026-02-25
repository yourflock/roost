<script lang="ts">
	interface Props {
		label: string;
		value: string | number;
		change?: number;
		changeLabel?: string;
		icon?: string;
		loading?: boolean;
	}

	let { label, value, change, changeLabel, icon, loading = false }: Props = $props();

	const isPositive = $derived(typeof change === 'number' && change >= 0);
</script>

<div class="stat-card">
	<div class="flex items-start justify-between">
		<div class="flex-1">
			<p class="text-sm font-medium text-slate-400">{label}</p>
			{#if loading}
				<div class="h-8 bg-slate-700 rounded animate-pulse w-20 mt-2"></div>
			{:else}
				<p class="text-2xl font-bold text-slate-100 mt-1">{value}</p>
			{/if}
			{#if change !== undefined}
				<p class="text-xs mt-1 {isPositive ? 'text-green-400' : 'text-red-400'}">
					{isPositive ? '+' : ''}{change}% {changeLabel ?? 'vs last week'}
				</p>
			{/if}
		</div>
		{#if icon}
			<div class="text-2xl text-slate-500">{icon}</div>
		{/if}
	</div>
</div>
