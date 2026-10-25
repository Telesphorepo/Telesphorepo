<!--
  One choice in a list of checkboxes or radio buttons: the control, its label, and a sentence
  that says what the choice does. The whole row is the hit target: a click anywhere on the
  control, the label or the description toggles the control. The control keeps the one label
  (the title), so the keyboard and screen readers work on the control as usual.

  The row has four looks, and a click shows its result at once under the pointer:
  - at rest: no fill;
  - under the pointer: the quiet hover fill;
  - chosen: the selected fill and the check of the control, a little deeper under the pointer;
  - off (a disabled control): muted text, no fill and no hover, the not-allowed cursor; say why
    in the description.

    <OptionRow id="v-private" label="Private" description="Only people you give access can download.">
      {#snippet control()}<RadioGroup.Item id="v-private" value="private" />{/snippet}
    </OptionRow>

  A list of rows keeps a 4px gap between them (`gap-1`), so the fills of two chosen rows stay apart.

  `icon` sits before the label, bare (a repository's FormatIcon). `children` render under the description, outside the hit target: a field, a note or a "Learn
  more" that belongs to the choice.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { Label } from "$lib/components/ui/label";

  let {
    id,
    label,
    description,
    control,
    icon,
    children,
  }: {
    /** The id of the control. */
    id: string;
    label: string;
    description?: string;
    control: Snippet;
    /** A bare 16px icon before the label: the FormatIcon of a repository. */
    icon?: Snippet;
    /** More under the description, e.g. a field or a warning. Clicks here do not toggle. */
    children?: Snippet;
  } = $props();

  // The control and the label handle their own clicks; a click elsewhere in the row clicks the
  // control once.
  const own = "button, a, input, select, textarea, label, [role=checkbox], [role=radio], [role=switch], [role=combobox]";
  function pick(e: MouseEvent) {
    const t = e.target as HTMLElement | null;
    if (!t || t.closest(own) || window.getSelection()?.toString()) return;
    document.getElementById(id)?.click();
  }
</script>

<div data-slot="option-row" class="option rounded-lg">
  <!-- The control is the keyboard target; the row only widens its pointer target. -->
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div data-hit class="hit flex cursor-pointer items-start gap-3 px-3 py-3" onclick={pick}>
    <div class="flex h-5 items-center">{@render control()}</div>
    <div class="flex min-w-0 flex-col gap-1">
      <div class="flex min-w-0 items-center gap-2">
        {#if icon}<span aria-hidden="true" class="flex shrink-0 text-muted-foreground">{@render icon()}</span>{/if}
        <Label for={id} class="cursor-[inherit] leading-5">{label}</Label>
      </div>
      {#if description}<p class="text-meta text-muted-foreground">{description}</p>{/if}
    </div>
  </div>
  {#if children}<div class="-mt-2 flex flex-col pr-3 pb-3 pl-10">{@render children()}</div>{/if}
</div>

<style>
  /* The states, in this order: a later one wins over an earlier one. A checkbox and a radio item
     say that they are chosen with data-state="checked". */
  .option {
    transition: background-color 100ms;
  }
  .option:has(> .hit:hover) {
    background: var(--hover);
  }
  .option:has(> .hit :global([data-state="checked"])) {
    background: var(--selected);
    color: var(--selected-foreground);
  }
  .option:has(> .hit:hover :global([data-state="checked"])) {
    background: color-mix(in oklch, var(--selected), var(--foreground) 6%);
  }
  .option:has(> .hit :global(:disabled)) {
    background: transparent;
    color: var(--muted-foreground);
  }
  .option:has(> .hit :global(:disabled)) > .hit {
    cursor: not-allowed;
  }
</style>
