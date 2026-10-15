<!--
  A yes/no setting as a row of the FormGrid: the label and one line of help in the label column,
  the switch and its state word ("On" / "Off") in the control column. Never a bare checkbox.

    <SwitchRow id="pw" label="Password sign-in for administrators"
      help="Administrators can also sign in with a password." checked={on} onCheckedChange={(v) => (on = v)} />
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { Switch } from "$lib/components/ui/switch";
  import FormRow from "./FormRow.svelte";

  let {
    id,
    label,
    help,
    checked = $bindable(false),
    disabled = false,
    onCheckedChange,
    onText = "On",
    offText = "Off",
    badge,
  }: {
    id: string;
    label: string;
    help?: string;
    checked?: boolean;
    disabled?: boolean;
    onCheckedChange?: (v: boolean) => void;
    onText?: string;
    offText?: string;
    badge?: Snippet;
  } = $props();
</script>

<FormRow {label} for={id} {help} width="auto" {badge}>
  <div class="flex h-9 items-center gap-3">
    <Switch
      {id}
      bind:checked
      {disabled}
      onCheckedChange={(v) => onCheckedChange?.(v)}
      aria-describedby={help ? `${id}-help` : undefined}
    />
    <span aria-hidden="true" class="text-body {checked ? '' : 'text-muted-foreground'}">{checked ? onText : offText}</span>
  </div>
</FormRow>
