<!-- Repository settings, Config tab: the format's config (custom editor, else JSON). -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo, type RepoConfig } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import Section from "$lib/components/Section.svelte";
  import ConfigEditor from "$lib/formats/ConfigEditor.svelte";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  // null: the editor holds an invalid config.
  let config = $state<RepoConfig | null>(null);
  let confirming = $state(false);
  let touched = $state(false);

  const current = $derived(repo.config ?? {});
  const changedKeys = $derived.by(() => {
    if (!config) return [];
    const keys = new Set([...Object.keys(current), ...Object.keys(config)]);
    return [...keys].filter((k) => JSON.stringify(current[k]) !== JSON.stringify(config![k])).sort();
  });
  const dirty = $derived(touched && config !== null && changedKeys.length > 0);

  const list = (v: unknown) => (Array.isArray(v) ? (v as string[]) : []);
  const changes = $derived.by(() => {
    const out = [`Saves the config of ${repo.name}`, ...changedKeys.map((k) => `Changes "${k}"`)];
    if (repo.format === "debian" && config && changedKeys.includes("architectures")) {
      const had = list(current.architectures);
      const has = list(config.architectures);
      const removed = had.filter((a) => !has.includes(a));
      const added = has.filter((a) => !had.includes(a));
      if (removed.length) out.push(`Deletes the index files of ${removed.join(", ")}`);
      if (added.length) out.push(`Rebuilds the indexes to add ${added.join(", ")}`);
    }
    return out;
  });
</script>

<Section narrow title="Config" description="Settings that belong to the {repo.format} format.">
  {#key repo.format + "/" + repo.name}
    <ConfigEditor
      format={repo.format}
      value={current}
      onChange={(v) => {
        config = v;
        touched = true;
      }}
    />
  {/key}
  <div class="mt-4 flex gap-2">
    <Button disabled={!dirty} onclick={() => (confirming = true)}>Save config</Button>
  </div>
</Section>

<ConfirmDialog
  bind:open={confirming}
  title="Save the config"
  confirmLabel="Save config"
  {changes}
  onConfirm={async () => {
    if (!config) return;
    await api.repos.update(repo.name, { config });
    toast.success("Saved the config of " + repo.name);
    touched = false;
    onChanged();
  }}
/>
