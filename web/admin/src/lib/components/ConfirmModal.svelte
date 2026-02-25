<script lang="ts">
	interface Props {
		open: boolean;
		title: string;
		message: string;
		confirmLabel?: string;
		cancelLabel?: string;
		danger?: boolean;
		loading?: boolean;
		onconfirm: () => void;
		oncancel: () => void;
	}

	let {
		open,
		title,
		message,
		confirmLabel = 'Confirm',
		cancelLabel = 'Cancel',
		danger = false,
		loading = false,
		onconfirm,
		oncancel
	}: Props = $props();
</script>

{#if open}
	<!-- Backdrop -->
	<div
		class="fixed inset-0 bg-black/60 backdrop-blur-sm z-50 flex items-center justify-center p-4"
		role="dialog"
		aria-modal="true"
		aria-labelledby="modal-title"
	>
		<div class="bg-slate-800 rounded-xl border border-slate-700 p-6 w-full max-w-md shadow-2xl">
			<h2 id="modal-title" class="text-lg font-semibold text-slate-100 mb-2">{title}</h2>
			<p class="text-slate-400 text-sm mb-6">{message}</p>
			<div class="flex gap-3 justify-end">
				<button class="btn-secondary" onclick={oncancel} disabled={loading}>
					{cancelLabel}
				</button>
				<button
					class={danger ? 'btn-danger' : 'btn-primary'}
					onclick={onconfirm}
					disabled={loading}
				>
					{#if loading}
						<span class="animate-pulse">Working...</span>
					{:else}
						{confirmLabel}
					{/if}
				</button>
			</div>
		</div>
	</div>
{/if}
