<!--
  Repository settings, Format settings: the options that only this format has, in the format's
  own editor (Debian: architectures) or as JSON, saved from the save bar. A Debian change of
  architectures asks first, because it rewrites the package lists. The Debian signing key has its
  own tab.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo, type RepoConfig } from "$lib/api";
  import { editors, formatLook } from "$lib/formats";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ConfigEditor from "$lib/formats/ConfigEditor.svelte";
  import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const formatTitle = $derived(formatLook(repo.format).title ?? repo.format);
  const current = $derived(repo.config ?? {});
  const hasOptions = $derived(repo.format in editors || Object.keys(current).length > 0);

  // undefined: no edit; null: the editor holds an invalid config. `round` restarts the editor.
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

  const list = (v: unknown) => (Array.isArray(v) ? (v as string[]) : []);
  const changes = $derived.by(() => {
    if (repo.format === "debian" && config && changedKeys.includes("architectures")) {
      const had = list(current.architectures);
      const has = list(config.architectures);
      const out: string[] = [];
      const added = has.filter((a) => !had.includes(a));
      const removed = had.filter((a) => !has.includes(a));
      if (added.length) out.push(`Adds ${added.join(", ")} to the package lists of ${repo.name}`);
      if (removed.length) out.push(`Removes ${removed.join(", ")} from the package lists; apt on those machines stops seeing updates`);
      out.push("Signs the package lists again");
      return out;
    }
    return changedKeys.map((k) => `Changes the option ${k}`);
  });

  function discard() {
    config = undefined;
    round++;
  }

  async function save() {
    if (config === null) throw new Error("Correct the format options first.");
    if (repo.format === "debian" && changedKeys.includes("architectures")) {
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

{#if !hasOptions}
  <CompactEmpty icon={SlidersIcon} text="{formatTitle} repositories have no options of their own." />
{:else}
  <FormSection title="{formatTitle} options">
    <FormGrid>
      {#key `${repo.format}/${repo.name}/${round}`}
        <ConfigEditor format={repo.format} value={current} onChange={(v) => (config = v)} />
      {/key}
    </FormGrid>
  </FormSection>
  <SaveBar {count} onDiscard={discard} onSave={save} />
{/if}

<ConfirmDialog bind:open={confirming} title="Save the format settings of {repo.name}?" confirmLabel="Save format settings" {changes} onConfirm={write} />
