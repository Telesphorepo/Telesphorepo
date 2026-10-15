<!--
  One line of text that never wraps: a long value ends in "…" and shows in full in a tooltip on
  hover or focus (only when it is cut). With `copy`, a small copy button follows the text; inside
  a DataTable row it appears when the pointer is over the row or the button has focus.

    <Truncate text={asset.path} copy />
    <Truncate text={key.fingerprint} copy={key.fingerprint} class="text-muted-foreground" />
-->
<script lang="ts">
  import * as Tooltip from "$lib/components/ui/tooltip";
  import CopyButton from "./CopyButton.svelte";
  import { cn } from "$lib/utils";

  let {
    text,
    copy = false,
    copyLabel,
    class: className = "",
  }: {
    text: string;
    /** true copies `text`; a string copies that value instead. */
    copy?: boolean | string;
    /** The copy button's name: "Copy path". */
    copyLabel?: string;
    class?: string;
  } = $props();

  let el = $state<HTMLElement | null>(null);
  let cut = $state(false);
  const measure = () => {
    if (el) cut = el.scrollWidth > el.clientWidth + 1;
  };
  const value = $derived(typeof copy === "string" ? copy : text);
</script>

<span class="flex min-w-0 items-center gap-1">
  <Tooltip.Root disabled={!cut}>
    <Tooltip.Trigger>
      {#snippet child({ props })}
        <span
          {...props}
          bind:this={el}
          onpointerenter={measure}
          onfocus={measure}
          class={cn("min-w-0 truncate", className)}>{text}</span
        >
      {/snippet}
    </Tooltip.Trigger>
    <!-- A path breaks after its slashes; a long word without one breaks anywhere. -->
    <Tooltip.Content class="max-w-md rounded-lg px-2.5 py-1.5 text-meta wrap-anywhere"
      >{#each text.split("/") as part, i (i)}{#if i > 0}/<wbr />{/if}{part}{/each}</Tooltip.Content
    >
  </Tooltip.Root>
  {#if copy}
    <CopyButton
      {value}
      label={copyLabel ?? "Copy"}
      size="icon-xs"
      class="shrink-0 text-muted-foreground opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
    />
  {/if}
</span>
