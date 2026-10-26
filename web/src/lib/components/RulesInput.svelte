<!--
  An ordered list of rules: each row a glob of repo paths and one of options. The first rule
  whose glob matches a path decides for it, so a row moves up with its arrow. A format's config
  field of type "rules" uses it (ConfigEditor), with valueKey naming the value in each row.

    <RulesInput id="rules" value={rules} options={["deny", "allow"]} valueKey="overwrite" onchange={(v) => (rules = v)} />
-->
<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
  import ArrowUpIcon from "@lucide/svelte/icons/arrow-up";
  import XIcon from "@lucide/svelte/icons/x";
  import PlusIcon from "@lucide/svelte/icons/plus";

  type Rule = Record<string, string>;

  let {
    id,
    value,
    options,
    valueKey,
    placeholder,
    describedby,
    label = "rule",
    onchange,
  }: {
    id: string;
    value: Rule[];
    options: string[];
    valueKey: string;
    placeholder?: string;
    describedby?: string;
    label?: string;
    onchange: (v: Rule[]) => void;
  } = $props();

  function set(i: number, key: string, v: string) {
    onchange(value.map((r, j) => (j === i ? { ...r, [key]: v } : r)));
  }

  function up(i: number) {
    const next = [...value];
    [next[i - 1], next[i]] = [next[i], next[i - 1]];
    onchange(next);
  }

  const remove = (i: number) => onchange(value.filter((_, j) => j !== i));
  const add = () => onchange([...value, { match: "", [valueKey]: options[0] ?? "" }]);
</script>

<div {id} class="flex flex-col gap-2" aria-describedby={describedby}>
  {#each value as r, i (i)}
    <div class="flex items-center gap-2">
      <Input
        class="min-w-0 flex-1 font-mono"
        spellcheck={false}
        autocomplete="off"
        aria-label={`Pattern of ${label} ${i + 1}`}
        aria-invalid={(r.match ?? "").trim() === "" ? true : undefined}
        placeholder={placeholder}
        value={r.match ?? ""}
        oninput={(e) => set(i, "match", e.currentTarget.value)}
      />
      <Select.Root type="single" value={r[valueKey] ?? ""} onValueChange={(v) => set(i, valueKey, v)}>
        <Select.Trigger class="w-28" aria-label={`Value of ${label} ${i + 1}`}>
          <span class="truncate">{r[valueKey] || "Choose"}</span>
        </Select.Trigger>
        <Select.Content>
          {#each options as o (o)}<Select.Item value={o} label={o}>{o}</Select.Item>{/each}
        </Select.Content>
      </Select.Root>
      <Button variant="ghost" size="icon" disabled={i === 0} aria-label={`Move ${label} ${i + 1} up`} onclick={() => up(i)}>
        <ArrowUpIcon />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        class="hover:bg-destructive/10 hover:text-destructive"
        aria-label={`Remove ${label} ${i + 1}`}
        onclick={() => remove(i)}
      >
        <XIcon />
      </Button>
    </div>
  {/each}
  <div>
    <Button variant="outline" size="sm" onclick={add}><PlusIcon />Add a {label}</Button>
  </div>
</div>
