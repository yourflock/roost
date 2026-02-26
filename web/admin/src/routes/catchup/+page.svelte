<script lang="ts">
	import { enhance } from '$app/forms';

	interface ChannelCatchupStatus {
		slug: string;
		name: string;
		catchup_enabled: boolean;
		retention_days: number;
		recording_hours: number;
		total_mb: number;
		oldest_date: string | null;
	}

	interface Props {
		data: { channels: ChannelCatchupStatus[] };
	}

	let { data }: Props = $props();
	let channels = $derived(data.channels);
	let cleanupLoading = $state(false);

	const totalStorageMB = $derived(channels.reduce((sum, c) => sum + c.total_mb, 0));
	const enabledCount = $derived(channels.filter((c) => c.catchup_enabled).length);
</script>

<svelte:head>
	<title>Catchup / DVR — Roost Admin</title>
</svelte:head>

<div class="p-6 max-w-7xl mx-auto">
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">Catchup / DVR</h1>
			<p class="text-slate-400 text-sm mt-1">
				{enabledCount} channels recording · {totalStorageMB.toFixed(1)} MB total storage
			</p>
		</div>
		<form
			method="POST"
			action="?/triggerCleanup"
			use:enhance={() => {
				cleanupLoading = true;
				return async ({ update }) => {
					cleanupLoading = false;
					update();
				};
			}}
		>
			<button type="submit" class="btn-secondary" disabled={cleanupLoading}>
				{cleanupLoading ? 'Running...' : 'Run Cleanup Now'}
			</button>
		</form>
	</div>

	<!-- Stats row -->
	<div class="grid grid-cols-3 gap-4 mb-6">
		<div class="bg-slate-800 rounded-lg p-4 border border-slate-700">
			<p class="text-slate-400 text-sm">Recording Channels</p>
			<p class="text-2xl font-bold text-slate-100 mt-1">{enabledCount}</p>
		</div>
		<div class="bg-slate-800 rounded-lg p-4 border border-slate-700">
			<p class="text-slate-400 text-sm">Total Storage</p>
			<p class="text-2xl font-bold text-slate-100 mt-1">
				{totalStorageMB >= 1024
					? `${(totalStorageMB / 1024).toFixed(1)} GB`
					: `${totalStorageMB.toFixed(1)} MB`}
			</p>
		</div>
		<div class="bg-slate-800 rounded-lg p-4 border border-slate-700">
			<p class="text-slate-400 text-sm">Total Channels</p>
			<p class="text-2xl font-bold text-slate-100 mt-1">{channels.length}</p>
		</div>
	</div>

	<!-- Channel table -->
	<div class="bg-slate-800 rounded-lg overflow-hidden border border-slate-700">
		<table class="w-full text-sm">
			<thead class="bg-slate-700/50">
				<tr>
					<th class="text-left p-3 text-slate-300 font-medium">Channel</th>
					<th class="text-left p-3 text-slate-300 font-medium">Recording</th>
					<th class="text-left p-3 text-slate-300 font-medium">Archive Hours</th>
					<th class="text-left p-3 text-slate-300 font-medium">Storage</th>
					<th class="text-left p-3 text-slate-300 font-medium">Retention</th>
					<th class="text-left p-3 text-slate-300 font-medium">Oldest Archive</th>
					<th class="text-right p-3 text-slate-300 font-medium">Actions</th>
				</tr>
			</thead>
			<tbody>
				{#each channels as ch (ch.slug)}
					<tr class="border-t border-slate-700/50 hover:bg-slate-700/20">
						<td class="p-3">
							<p class="text-slate-100 font-medium">{ch.name}</p>
							<p class="text-slate-500 text-xs">{ch.slug}</p>
						</td>
						<td class="p-3">
							<span
								class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-medium {ch.catchup_enabled
									? 'bg-green-900/30 text-green-400'
									: 'bg-slate-700 text-slate-400'}"
							>
								{#if ch.catchup_enabled}
									<span class="w-1.5 h-1.5 rounded-full bg-green-400 animate-pulse"></span>
									Recording
								{:else}
									Disabled
								{/if}
							</span>
						</td>
						<td class="p-3 text-slate-300">{ch.recording_hours}h</td>
						<td class="p-3 text-slate-300">
							{ch.total_mb >= 1024
								? `${(ch.total_mb / 1024).toFixed(1)} GB`
								: `${ch.total_mb.toFixed(1)} MB`}
						</td>
						<td class="p-3 text-slate-300">{ch.retention_days} days</td>
						<td class="p-3 text-slate-500 text-xs">{ch.oldest_date ?? 'No archive'}</td>
						<td class="p-3 text-right">
							<form
								method="POST"
								action="?/updateSettings"
								use:enhance
								class="flex items-center justify-end gap-2"
							>
								<input type="hidden" name="channel_slug" value={ch.slug} />
								<input type="hidden" name="enabled" value={!ch.catchup_enabled} />
								<input type="hidden" name="retention_days" value={ch.retention_days} />
								<button
									type="submit"
									class="text-xs {ch.catchup_enabled
										? 'text-yellow-400 hover:text-yellow-300'
										: 'text-blue-400 hover:text-blue-300'}"
								>
									{ch.catchup_enabled ? 'Disable' : 'Enable'}
								</button>
							</form>
						</td>
					</tr>
				{:else}
					<tr>
						<td colspan="7" class="p-8 text-center text-slate-500"> No channels found. </td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</div>
