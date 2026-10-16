<!--
  The repo-config editor of a format: one FormRow (or SwitchRow) per field of the format's
  descriptor (config.fields), by field type: a text input, a ListInput, a select, a switch. Keys of
  the config that no field names are kept as they are. onChange gets the new config, or null
  while a required field is empty. Wrap it in {#key format + name} so its state starts again when
  the format or the repo changes; put it in a FormGrid.

    <FormGrid><ConfigEditor fields={format.config.fields} value={repo.config} onChange={(v) => (config = v)} /></FormGrid>
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type { FormatField, RepoConfig } from "$lib/api";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
  import FormRow from "$lib/components/FormRow.svelte";
  import SwitchRow from "$lib/components/SwitchRow.svelte";
  import ListInput from "$lib/components/ListInput.svelte";

  let { fields, value, onChange }: { fields: FormatField[]; value: RepoConfig; onChange: (v: RepoConfig | null) => void } = $props();

  const id = $props.id();
  // The editor owns its values after the first render; wrap it in {#key} to start over.
  let config = $state<RepoConfig>(untrack(() => ({ ...value })));

  const text = (k: string) => (typeof config[k] === "string" ? (config[k] as string) : config[k] == null ? "" : String(config[k]));
  const list = (k: string) => (Array.isArray(config[k]) ? (config[k] as string[]) : []);
  const missing = (f: FormatField) => !!f.required && (f.type === "list" ? list(f.key).length === 0 : text(f.key).trim() === "");

  function set(f: FormatField, v: unknown) {
    const next = { ...config };
    if ((f.type === "string" || f.type === "secret") && v === "" && !f.required) delete next[f.key];
    else next[f.key] = v;
    config = next;
    onChange(fields.some(missing) ? null : next);
  }
</script>

{#each fields as f (f.key)}
  {@const fid = `${id}-${f.key}`}
  {@const err = missing(f) ? `Enter at least one value.` : undefined}
  {#if f.type === "bool"}
    <SwitchRow id={fid} label={f.label} help={f.help} checked={config[f.key] === true} onCheckedChange={(v) => set(f, v)} />
  {:else}
    <FormRow label={f.label} for={fid} help={f.help} error={err}>
      {#if f.type === "list"}
        <ListInput
          id={fid}
          values={list(f.key)}
          placeholder={f.placeholder ? `Add a value, e.g. ${f.placeholder}` : undefined}
          normalize={(s) => s.trim()}
          describedby={f.help ? `${fid}-help` : undefined}
          invalid={!!err}
          label={f.label.toLowerCase()}
          onchange={(v) => set(f, v)}
        />
      {:else if f.type === "enum"}
        <Select.Root type="single" value={text(f.key)} onValueChange={(v) => set(f, v)}>
          <Select.Trigger id={fid} class="w-full" aria-describedby={f.help ? `${fid}-help` : undefined}>
            <span class="truncate">{text(f.key) || "Choose"}</span>
          </Select.Trigger>
          <Select.Content>
            {#each f.options ?? [] as o (o)}<Select.Item value={o} label={o}>{o}</Select.Item>{/each}
          </Select.Content>
        </Select.Root>
      {:else}
        <Input
          id={fid}
          type={f.type === "secret" ? "password" : "text"}
          spellcheck={false}
          autocomplete="off"
          placeholder={f.placeholder}
          aria-describedby={f.help ? `${fid}-help` : undefined}
          aria-invalid={err ? true : undefined}
          value={text(f.key)}
          oninput={(e) => set(f, e.currentTarget.value)}
        />
      {/if}
    </FormRow>
  {/if}
{/each}
