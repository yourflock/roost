<script lang="ts">
	import { enhance } from '$app/forms';
	import ConfirmModal from '$lib/components/ConfirmModal.svelte';

	interface ActiveStream {
		id: string;
		subscriber_id: string;
		subscriber_email: string;
		channel_id: string;
		channel_name: string;
		started_at: string;
		quality: string;
		bitrate_kbps: number;
		user_agent: string;
	}

	interface Props {
		data: { streams: ActiveStream[] };
		form: { error?: string; success?: boolean } | null;
	}

	let { data, form }: Props = $props();

	let streams = $state(data.streams);
	let terminateTarget = $state<ActiveStream | null>(null);
	let terminateLoading = $state(false);

	function formatDuration(startedAt: string): string {
		const ms = Date.now() - new Date(startedAt).getTime();
		const mins = Math.floor(ms / 60000);
		const hrs = Math.floor(mins / 60);
		if (hrs > 0) return `${hrs}h ${mins % 60}m`;
		return `${mins}m`;
	}

	function formatBitrate(kbps: number): string {
		if (kbps >= 1000) return `${(kbps / 1000).toFixed(1)} Mbps`;
		return `${kbps} kbps`;
	}
</script>

<svelte:head>
	<title>Live Streams — Roost Admin</title>
</svelte:head>

<ConfirmModal
	open={!!terminateTarget}
	title="Terminate Stream"
	message="Force-terminate the stream for {terminateTarget?.subscriber_email} watching {terminateTarget?.channel_name}?"
	confirmLabel="Terminate"
	danger
	loading={terminateLoading}
	onconfirm={() => {
		if (!terminateTarget) return;
		terminateLoading = true;
		const form = document.getElementById('terminate-form') as HTMLFormElement;
		const input = form.querySelector('input[name="stream_id"]') as HTMLInputElement;
		input.value = terminateTarget.id;
		form.requestSubmit();
	}}
	oncancel={() => (terminateTarget = null)}
/>

<form id="terminate-form" method="POST" action="?/terminate" use:enhance={() => {
	return async ({ update }) => { terminateLoading = false; terminateTarget = null; update(); };
}}>
	<input type="hidden" name="stream_id" value="" />
</form>

<div class="p-6 max-w-7xl mx-auto">
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-slate-100">Live Streams</h1>
			<p class="text-slate-400 text-sm mt-1">
				{streams.length} active stream{streams.length !== 1 ? 's' : ''}
			</p>
		</div>
		<button
			class="btn-secondary btn-sm"
			onclick={() => window.location.reload()}
		>
			Refresh
		</button>
	</div>

	{#if form?.error}
		<div class="bg-red-500/10 border border-red-500/30 text-red-400 text-sm px-4 py-3 rounded-lg mb-4">
			{form.error}
		</div>
	{/if}

	{#if streams.length === 0}
		<div class="card text-center py-12">
			<div class="text-4xl mb-3">📺</div>
			<p class="text-slate-400">No active streams right now.</p>
		</div>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-700">
			<table class="w-full text-left">
				<thead class="bg-slate-800/80 border-b border-slate-700">
					<tr>
						<th class="table-header">Subscriber</th>
						<th class="table-header">Channel</th>
						<th class="table-header">Duration</th>
						<th class="table-header">Quality</th>
						<th class="table-header">Bitrate</th>
						<th class="table-header">Client</th>
						<th class="table-header"></th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-700/50">
					{#each streams as stream}
						<tr class="table-row">
							<td class="table-cell">
								<div>
									<a href="/subscribers/{stream.subscriber_id}" class="text-roost-400 hover:underline text-sm">
										{stream.subscriber_email}
									</a>
								</div>
							</td>
							<td class="table-cell">
								<div class="flex items-center gap-2">
									<span class="w-2 h-2 rounded-full bg-red-500 animate-pulse"></span>
									<span class="font-medium text-slate-100">{stream.channel_name}</span>
								</div>
							</td>
							<td class="table-cell text-slate-300">{formatDuration(stream.started_at)}</td>
							<td class="table-cell">
								<code class="text-xs text-slate-300 bg-slate-700/50 px-2 py-0.5 rounded">{stream.quality}</code>
							</td>
							<td class="table-cell text-slate-300">{formatBitrate(stream.bitrate_kbps)}</td>
							<td class="table-cell">
								<span class="text-xs text-slate-500 truncate max-w-[160px] block">{stream.user_agent}</span>
							</td>
							<td class="table-cell">
								<button class="btn-danger btn-sm" onclick={() => (terminateTarget = stream)}>
									Terminate
								</button>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	<p class="text-xs text-slate-500 mt-4">
		Page auto-refreshed when you loaded it. Click Refresh for the latest data.
	</p>
</div>
