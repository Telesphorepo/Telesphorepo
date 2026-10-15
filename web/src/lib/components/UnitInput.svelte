<!--
  A number with its unit as one control, the same width as every other text control of a form:
  the number fills the box and the unit is a menu at its end ("2 · GiB", "15 · minutes"). Units
  come from lib/units.ts (SIZE_UNITS, DURATION_UNITS) or any {value, label} list.

    <FormRow label="Largest upload" for="max-upload" help="Uploads above this size are refused.">
      <UnitInput id="max-upload" bind:num bind:unit units={SIZE_UNITS} describedby="max-upload-help" />
    </FormRow>
-->
<script lang="ts">
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Select from "$lib/components/ui/select";

  let {
    id,
    num = $bindable(""),
    unit = $bindable(""),
    units,
    label,
    placeholder,
    disabled = false,
    invalid = false,
    describedby,
    onchange,
  }: {
    id: string;
    num?: string;
    unit?: string;
    units: { value: string; label: string }[];
    /** Names the unit menu for screen readers: "<label> unit". */
    label?: string;
    placeholder?: string;
    disabled?: boolean;
    invalid?: boolean;
    describedby?: string;
    /** Called after either part changes. */
    onchange?: (v: { num: string; unit: string }) => void;
  } = $props();

  const unitLabel = $derived(units.find((u) => u.value === unit)?.label ?? unit);
</script>

<InputGroup.Root data-disabled={disabled || undefined} class="overflow-hidden">
  <InputGroup.Input
    {id}
    type="number"
    min="0"
    step="any"
    inputmode="decimal"
    autocomplete="off"
    class="tabular-nums"
    {disabled}
    {placeholder}
    bind:value={() => num, (v) => { num = String(v ?? ""); onchange?.({ num, unit }); }}
    aria-invalid={invalid || undefined}
    aria-describedby={describedby}
  />
  <Select.Root type="single" {disabled} value={unit} onValueChange={(v) => { unit = v; onchange?.({ num, unit }); }}>
    <Select.Trigger
      aria-label={label ? `${label} unit` : "Unit"}
      class="h-full w-28 shrink-0 rounded-none border-0 border-l border-input bg-transparent pr-3 pl-3 shadow-none hover:bg-hover focus-visible:ring-0 focus-visible:bg-hover dark:bg-transparent dark:hover:bg-hover"
    >
      <span class="truncate">{unitLabel}</span>
    </Select.Trigger>
    <Select.Content align="end">
      {#each units as u (u.value)}
        <Select.Item value={u.value} label={u.label}>{u.label}</Select.Item>
      {/each}
    </Select.Content>
  </Select.Root>
</InputGroup.Root>
