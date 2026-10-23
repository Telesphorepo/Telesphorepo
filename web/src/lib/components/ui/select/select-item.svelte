<script lang="ts">
	import type { Snippet } from "svelte";
	import { Select as SelectPrimitive } from "bits-ui";
	import CheckIcon from '@lucide/svelte/icons/check';
	import { cn, type WithoutChild } from "$lib/utils.js";

	let {
		ref = $bindable(null),
		class: className,
		value,
		label,
		icon,
		inset = false,
		children: childrenProp,
		...restProps
	}: WithoutChild<SelectPrimitive.ItemProps> & {
		/** The choice's mark before its label: a FormatIcon, a small UserAvatar, a meaning icon. */
		icon?: Snippet;
		/** Keep the icon's space empty, so the label lines up with the items that have one ("All formats"). */
		inset?: boolean;
	} = $props();
</script>

<SelectPrimitive.Item
	bind:ref
	{value}
	{label}
	data-slot="select-item"
	class={cn(
		"focus:bg-accent focus:text-accent-foreground not-data-[variant=destructive]:focus:**:text-accent-foreground gap-2.5 rounded-xl py-2 pr-8 pl-3 text-sm [&_svg:not([class*='size-'])]:size-4 *:[span]:last:flex *:[span]:last:items-center *:[span]:last:gap-2 relative flex w-full cursor-default items-center outline-hidden select-none data-disabled:pointer-events-none data-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0",
		"data-highlighted:bg-accent data-highlighted:text-accent-foreground",
		className
	)}
	{...restProps}
>
	{#snippet children({ selected, highlighted })}
		<span class="pointer-events-none absolute right-2 flex size-4 items-center justify-center">
			{#if selected}
				<CheckIcon class="pointer-events-none" />
			{/if}
		</span>
		<span class="flex min-w-0 flex-1 gap-2 whitespace-nowrap">
			{#if icon}
				<span class="flex min-w-4 shrink-0 items-center justify-center">{@render icon()}</span>
			{:else if inset}
				<span class="w-4 shrink-0" aria-hidden="true"></span>
			{/if}
			{#if childrenProp}
				{@render childrenProp({ selected, highlighted })}
			{:else}
				<span class="truncate">{label || value}</span>
			{/if}
		</span>
	{/snippet}
</SelectPrimitive.Item>
