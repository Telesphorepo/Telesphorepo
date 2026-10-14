<!--
  One IP allow-list of a repository ("Who can download" or "Who can publish"): preset checkboxes
  and address ranges with notes. The parent holds the list and saves it.

    <IPRuleList id="read" title="Who can download" {presets} bind:rules={draft.read} showErrors={tried} />

  presets are the ones offered for rules; a stored preset that is not offered still shows, so it
  can be removed. errorsOf(rules) gives the problems that stop a save.
-->
<script lang="ts" module>
  import { checkRange } from "$lib/network";
  import type { IPRule } from "$lib/api";

  /** A preset offered for rules, with the words that explain it. */
  export type OfferedPreset = { name: string; title: string; help: string; warning?: string };

  export const maxRules = 200;
  export const maxNote = 200;

  /** The problem of each range row by index ("" when fine), plus duplicates and the count limit. */
  export function rowErrors(rules: IPRule[]): string[] {
    const seen = new Set<string>();
    return rules.map((r) => {
      if (r.kind !== "cidr") return "";
      const v = r.value.trim();
      const bad = checkRange(v);
      if (bad) return bad;
      if (seen.has(v)) return `${v} is in the list twice. Remove one.`;
      seen.add(v);
      if ([...r.note].length > maxNote) return `Shorten the note to ${maxNote} characters or fewer.`;
      return "";
    });
  }

  /** Every problem that stops a save, for the list called label. */
  export function errorsOf(label: string, rules: IPRule[]): string[] {
    const out = rowErrors(rules).filter(Boolean).map((e) => `${label}: ${e}`);
    if (rules.length > maxRules) out.push(`${label}: use at most ${maxRules} entries.`);
    return out;
  }
</script>

<script lang="ts">
  import { tick } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { rangeSize } from "$lib/network";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";

  let {
    id,
    title,
    description,
    openText,
    presets,
    rules = $bindable([]),
    showErrors = false,
    disabled = false,
  }: {
    /** Prefix of the element ids, unique on the page. */
    id: string;
    title: string;
    description: string;
    /** What an empty list means, e.g. "Anyone with the right account can download, from any address." */
    openText: string;
    presets: OfferedPreset[];
    rules?: IPRule[];
    /** Shows the problems of empty rows too (after the user tried to save). */
    showErrors?: boolean;
    disabled?: boolean;
  } = $props();

  // Offered presets, then stored presets the page does not offer (they can only be removed).
  const shownPresets = $derived([
    ...presets,
    ...rules
      .filter((r) => r.kind === "preset" && !presets.some((p) => p.name === r.value))
      .map((r) => ({ name: r.value, title: r.value, help: "A preset this page does not offer. Untick it to remove it." }) as OfferedPreset),
  ]);
  const hasPreset = (name: string) => rules.some((r) => r.kind === "preset" && r.value === name);

  function togglePreset(name: string, on: boolean) {
    if (on && !hasPreset(name)) rules = [...rules, { kind: "preset", value: name, note: "" }];
    if (!on) rules = rules.filter((r) => !(r.kind === "preset" && r.value === name));
  }

  // The range rows with their index in rules, so inputs bind to the right entry.
  const ranges = $derived(rules.map((r, i) => ({ r, i })).filter((x) => x.r.kind === "cidr"));
  const errors = $derived(rowErrors(rules));

  function addRange() {
    rules = [...rules, { kind: "cidr", value: "", note: "" }];
    // Focus the new address field once it renders.
    const n = rules.length - 1;
    tick().then(() => document.getElementById(`${id}-range-${n}`)?.focus());
  }

  const removeAt = (i: number) => (rules = rules.filter((_, j) => j !== i));
</script>

<fieldset class="flex flex-col gap-4">
  <legend class="text-sm font-semibold">{title}</legend>
  <p class="-mt-2 max-w-prose text-sm text-muted-foreground">{description}</p>

  {#if rules.length === 0}
    <p class="rounded-xl bg-muted/60 px-4 py-3 text-sm">{openText}</p>
  {/if}

  <div class="flex flex-col gap-1">
    {#each shownPresets as p (p.name)}
      <div class="flex items-start gap-3 rounded-lg px-2 py-2 hover:bg-hover has-data-[state=checked]:bg-selected has-data-[state=checked]:text-selected-foreground">
        <Checkbox
          id="{id}-preset-{p.name}"
          class="mt-0.5"
          checked={hasPreset(p.name)}
          {disabled}
          onCheckedChange={(v) => togglePreset(p.name, v === true)}
        />
        <div class="flex min-w-0 flex-col gap-1">
          <Label for="{id}-preset-{p.name}">{p.title}</Label>
          <p class="text-sm text-muted-foreground">{p.help}</p>
          {#if p.warning && hasPreset(p.name)}<p class="text-sm text-destructive">{p.warning}</p>{/if}
        </div>
      </div>
    {/each}
  </div>

  {#if ranges.length}
    <ul class="flex flex-col gap-3">
      {#each ranges as { r, i } (i)}
        {@const err = errors[i] && (showErrors || r.value.trim() !== "") ? errors[i] : ""}
        <li class="flex flex-col gap-2">
          <div class="flex flex-wrap items-end gap-2">
            <div class="flex flex-col gap-2">
              <Label for="{id}-range-{i}" class="sr-only">Address or range</Label>
              <Input
                id="{id}-range-{i}"
                class="w-56 font-mono"
                placeholder="203.0.113.0/24"
                autocomplete="off"
                spellcheck={false}
                {disabled}
                aria-invalid={err ? true : undefined}
                bind:value={rules[i].value}
              />
            </div>
            <div class="flex min-w-0 flex-1 flex-col gap-2">
              <Label for="{id}-note-{i}" class="sr-only">Note</Label>
              <Input
                id="{id}-note-{i}"
                class="max-w-sm"
                placeholder="Note, e.g. Office network"
                autocomplete="off"
                maxlength={maxNote}
                {disabled}
                bind:value={rules[i].note}
              />
            </div>
            <Button variant="ghost" size="icon" {disabled} aria-label="Remove {r.value || 'this range'}" onclick={() => removeAt(i)}>
              <XIcon />
            </Button>
          </div>
          {#if err}
            <ErrorState inline error={err} />
          {:else if r.value.trim()}
            <p class="text-xs text-muted-foreground">Covers {rangeSize(r.value)}.</p>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}

  <div>
    <Button variant="outline" size="sm" disabled={disabled || rules.length >= maxRules} onclick={addRange}>
      <PlusIcon />Add an address or range
    </Button>
  </div>
</fieldset>
