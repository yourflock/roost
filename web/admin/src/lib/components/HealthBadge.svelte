<script lang="ts">
	interface Props {
		status: 'healthy' | 'degraded' | 'down' | 'syncing' | 'idle' | 'success' | 'error';
		label?: string;
	}

	let { status, label }: Props = $props();

	const config = $derived(() => {
		switch (status) {
			case 'healthy':
			case 'success':
				return { cls: 'badge-online', dot: 'bg-green-400', text: label ?? status };
			case 'degraded':
			case 'syncing':
			case 'idle':
				return { cls: 'badge-degraded', dot: 'bg-yellow-400', text: label ?? status };
			case 'down':
			case 'error':
				return { cls: 'badge-offline', dot: 'bg-red-400', text: label ?? status };
			default:
				return { cls: 'badge-degraded', dot: 'bg-slate-400', text: label ?? status };
		}
	});
</script>

<span class="{config().cls} flex items-center gap-1.5 w-fit">
	<span class="w-1.5 h-1.5 rounded-full {config().dot}"></span>
	{config().text}
</span>
