<script lang="ts">
	import HealthBadge from '$lib/components/HealthBadge.svelte';

	interface ServiceHealth {
		name: string;
		status: 'healthy' | 'degraded' | 'down';
		latency_ms: number | null;
		details: string | null;
		checked_at: string;
	}

	interface Props {
		data: { services: ServiceHealth[]; checkedAt: string };
	}

	let { data }: Props = $props();

	const overallStatus = $derived(() => {
		if (data.services.some((s) => s.status === 'down')) return 'down';
		if (data.services.some((s) => s.status === 'degraded')) return 'degraded';
		if (data.services.length === 0) return 'degraded';
		return 'healthy';
	});

	function formatTime(d: string): string {
		return new Date(d).toLocaleTimeString('en-US', {
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit'
		});
	}
</script>

<svelte:head>
	<title>System Health — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-4xl mx-auto">
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">System Health</h1>
			<p class="text-slate-400 text-sm mt-1">
				Last checked: {formatTime(data.checkedAt)}
			</p>
		</div>
		<div class="flex items-center gap-3">
			<HealthBadge
				status={overallStatus()}
				label={overallStatus() === 'healthy'
					? 'All systems operational'
					: overallStatus() === 'degraded'
						? 'Partial outage'
						: 'Service disruption'}
			/>
			<button class="btn-secondary btn-sm" onclick={() => window.location.reload()}>
				Refresh
			</button>
		</div>
	</div>

	{#if data.services.length === 0}
		<div class="card text-center py-12">
			<div class="text-4xl mb-3">🖥️</div>
			<p class="text-slate-400">Cannot connect to Roost API to check service health.</p>
			<p class="text-slate-500 text-sm mt-1">Make sure backend services are running.</p>
		</div>
	{:else}
		<div class="space-y-3">
			{#each data.services as svc}
				<div class="card flex items-center justify-between">
					<div class="flex items-center gap-4">
						<div>
							<p class="font-medium text-slate-100">{svc.name}</p>
							{#if svc.details}
								<p class="text-xs text-slate-400 mt-0.5">{svc.details}</p>
							{/if}
						</div>
					</div>
					<div class="flex items-center gap-4">
						{#if svc.latency_ms !== null}
							<span class="text-sm text-slate-400">
								{svc.latency_ms}ms
							</span>
						{/if}
						<HealthBadge status={svc.status} />
					</div>
				</div>
			{/each}
		</div>

		<!-- Summary -->
		<div class="mt-6 grid grid-cols-3 gap-4">
			<div class="card text-center">
				<p class="text-2xl font-bold text-green-400">
					{data.services.filter((s) => s.status === 'healthy').length}
				</p>
				<p class="text-sm text-slate-400 mt-1">Healthy</p>
			</div>
			<div class="card text-center">
				<p class="text-2xl font-bold text-yellow-400">
					{data.services.filter((s) => s.status === 'degraded').length}
				</p>
				<p class="text-sm text-slate-400 mt-1">Degraded</p>
			</div>
			<div class="card text-center">
				<p class="text-2xl font-bold text-red-400">
					{data.services.filter((s) => s.status === 'down').length}
				</p>
				<p class="text-sm text-slate-400 mt-1">Down</p>
			</div>
		</div>
	{/if}
</div>
