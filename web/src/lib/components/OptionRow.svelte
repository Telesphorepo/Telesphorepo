<!--
  One choice in a list of checkboxes or radio buttons: the control, its label, and a sentence
  that says what the choice does. The whole row is the hit target: a click anywhere on the
  control, the label or the description toggles the control. The row fills on hover and when it
  is chosen. The control keeps the one label (the title), so the keyboard and screen readers
  work on the control as usual.

    <OptionRow id="v-private" label="Private" description="Only people you give access can download.">
      {#snippet control()}<RadioGroup.Item id="v-private" value="private" />{/snippet}
    </OptionRow>

  `children` render under the description, outside the hit target: a field, a note or a "Learn
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
    children,
  }: {
    /** The id of the control. */
    id: string;
    label: string;
    description?: string;
    control: Snippet;
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

<div class="rounded-lg transition-colors has-[[data-hit]:hover]:bg-hover has-data-checked:bg-selected has-data-checked:text-selected-foreground">
  <!-- The control is the keyboard target; the row only widens its pointer target. -->
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div data-hit class="flex cursor-pointer items-start gap-3 px-3 py-3 has-disabled:cursor-not-allowed" onclick={pick}>
    <div class="flex h-5 items-center">{@render control()}</div>
    <div class="flex min-w-0 flex-col gap-1">
      <Label for={id} class="cursor-[inherit] leading-5">{label}</Label>
      {#if description}<p class="text-meta text-muted-foreground">{description}</p>{/if}
    </div>
  </div>
  {#if children}<div class="-mt-2 flex flex-col pr-3 pb-3 pl-10">{@render children()}</div>{/if}
</div>
