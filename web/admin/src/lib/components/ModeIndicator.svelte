<script lang="ts">
	import { onMount } from 'svelte';

	interface Props {
		/** Roost operating mode. If not provided, fetched from /system/info on mount. */
		mode?: 'private' | 'public' | null;
		/** Show feature flag pills alongside the mode badge. Default: false. */
		showFeatures?: boolean;
	}

	let { mode = null, showFeatures = false }: Props = $props();

	interface SystemInfo {
		mode: 'private' | 'public';
		version: string;
		features: {
			subscriber_management: boolean;
			billing: boolean;
			cdn_relay: boolean;
		};
	}

	let info = $state<SystemInfo | null>(null);
	let fetchError = $state<string | null>(null);

	const resolvedMode = $derived(mode ?? info?.mode ?? null);

	const badgeConfig = $derived(() => {
		switch (resolvedMode) {
			case 'public':
				return {
					cls: 'bg-green-100 text-green-800 ring-green-200',
					dot: 'bg-green-500',
					label: 'Public Mode',
					title: 'Subscriber management, billing, and Owl addon API are active.'
				};
			case 'private':
				return {
					cls: 'bg-amber-100 text-amber-800 ring-amber-200',
					dot: 'bg-amber-500',
					label: 'Private Mode',
					title: 'Self-hosted mode — billing and Owl addon API are disabled.'
				};
			default:
				return {
					cls: 'bg-gray-100 text-gray-500 ring-gray-200',
					dot: 'bg-gray-400',
					label: 'Loading…',
					title: 'Fetching mode from /system/info'
				};
		}
	});

	onMount(async () => {
		// Only auto-fetch if no mode prop was supplied.
		if (mode !== null) return;

		try {
			const resp = await fetch('/system/info');
			if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
			info = await resp.json();
		} catch (err) {
			fetchError = err instanceof Error ? err.message : 'unknown error';
		}
	});
</script>

<!-- ModeIndicator.svelte — Shows ROOST_MODE in the admin UI header. -->
<!-- P20.1.002: Mode indicator in admin UI -->
<!--
  Displays a persistent badge indicating whether the Roost instance is running
  in private (self-hosted) or public (managed/subscriber) mode.

  Private mode: amber badge — billing and Owl addon API are disabled.
  Public mode:  green badge  — full subscriber stack active.

  Usage:
    <ModeIndicator mode="private" />
    <ModeIndicator mode="public" />
    <ModeIndicator /> <!-- auto-fetches from /system/info -->
-->

<!-- Mode badge -->
<div class="inline-flex items-center gap-2">
	<span
		class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium ring-1 ring-inset {badgeConfig()
			.cls}"
		title={badgeConfig().title}
	>
		<span class="h-1.5 w-1.5 rounded-full {badgeConfig().dot}"></span>
		{badgeConfig().label}
	</span>

	{#if fetchError}
		<span class="text-xs text-red-500" title="Could not fetch /system/info: {fetchError}">
			(fetch failed)
		</span>
	{/if}

	{#if showFeatures && info}
		<span class="flex items-center gap-1">
			{#each Object.entries(info.features) as [key, enabled]}
				<span
					class="inline-flex items-center rounded px-1.5 py-0.5 text-xs font-mono
					       {enabled
						? 'bg-green-50 text-green-700 ring-1 ring-inset ring-green-200'
						: 'bg-gray-50 text-gray-400 ring-1 ring-inset ring-gray-200 line-through'}"
					title="{key}: {enabled ? 'enabled' : 'disabled'}"
				>
					{key.replace(/_/g, '-')}
				</span>
			{/each}
		</span>
	{/if}
</div>
