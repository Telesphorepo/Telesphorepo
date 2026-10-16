<!--
  Repository settings, Format settings: the config options of the repository's format, one field
  per entry of config.fields of its descriptor (ConfigEditor), saved from the save bar. A change of
  a field with a change_note asks first and lists the note, e.g. the Debian architectures, whose
  change rewrites the package lists.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo, type RepoConfig } from "$lib/api";
  import { formats } from "$lib/formats/registry.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ConfigEditor from "$lib/formats/ConfigEditor.svelte";
  import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const format = $derived(formats.get(repo.format));
  const fields = $derived(format?.config.fields ?? []);
  const current = $derived(repo.config ?? {});

  // undefined: no edit; null: a required field is empty. `round` restarts the editor.
  let config = $state<RepoConfig | null | undefined>(undefined);
  let round = $state(0);
  let confirming = $state(false);

  const changedKeys = $derived.by(() => {
    if (!config) return [];
    const keys = new Set([...Object.keys(current), ...Object.keys(config)]);
    return [...keys].filter((k) => JSON.stringify(current[k]) !== JSON.stringify(config![k])).sort();
  });
  // An invalid config counts as one change, so the page asks before it is left.
  const count = $derived(config === null ? 1 : changedKeys.length);
  const noted = $derived(fields.filter((f) => f.change_note && changedKeys.includes(f.key)));

  const list = (v: unknown) => (Array.isArray(v) ? (v as string[]) : []);
  const changes = $derived.by(() => {
    if (!config) return [];
    const out: string[] = [];
    for (const k of changedKeys) {
      const f = fields.find((x) => x.key === k);
      const name = f?.label ?? k;
      if (f?.type === "list") {
        const had = list(current[k]);
        const has = list(config[k]);
        const added = has.filter((a) => !had.includes(a));
        const removed = had.filter((a) => !has.includes(a));
        if (added.length) out.push(`Adds ${added.join(", ")} to ${name}`);
        if (removed.length) out.push(`Removes ${removed.join(", ")} from ${name}`);
      } else out.push(`Changes ${name}`);
      if (f?.change_note) out.push(f.change_note);
    }
    return out;
  });

  function discard() {
    config = undefined;
    round++;
  }

  async function save() {
    if (config === null) throw new Error("Fill in the options that need a value first.");
    if (noted.length) {
      confirming = true;
      return;
    }
    await write();
  }

  async function write() {
    if (!config) return;
    await api.repos.update(repo.name, { config });
    toast.success(`Saved the format settings of ${repo.name}`);
    config = undefined;
    round++;
    onChanged();
  }
</script>

{#if !fields.length}
  <CompactEmpty icon={SlidersIcon} text="{formats.title(repo.format)} repositories have no options of their own." />
{:else}
  <FormSection title="{formats.title(repo.format)} options">
    <FormGrid>
      {#key `${repo.format}/${repo.name}/${round}`}
        <ConfigEditor {fields} value={current} onChange={(v) => (config = v)} />
      {/key}
    </FormGrid>
  </FormSection>
  <SaveBar {count} onDiscard={discard} onSave={save} />
{/if}

<ConfirmDialog bind:open={confirming} title="Save the format settings of {repo.name}?" confirmLabel="Save format settings" {changes} onConfirm={write} />
