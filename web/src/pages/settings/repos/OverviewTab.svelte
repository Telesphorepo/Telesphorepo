<!--
  Repository settings, Overview: its description, saved from the save bar, and what the
  repository holds (files, size, last publish and the format's own facts).
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { formatCount, formatRelative, formatSize } from "$lib/format";
  import { Textarea } from "$lib/components/ui/textarea";
  import Section from "$lib/components/Section.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const id = $props.id();
  const max = 500;
  const draft = new Draft({ description: untrack(() => repo.description) });
  const status = new Resource(() => api.repos.status(repo.name));

  const factLabel = (k: string) => {
    const s = k.replace(/_/g, " ");
    return s.charAt(0).toUpperCase() + s.slice(1);
  };
  const factValue = (v: unknown): string =>
    typeof v === "number"
      ? formatCount(v)
      : typeof v === "boolean"
        ? v
          ? "yes"
          : "no"
        : typeof v === "string"
          ? v || "none"
          : Array.isArray(v)
            ? v.join(", ") || "none"
            : JSON.stringify(v);
  const facts = $derived([
    { label: "Files", value: formatCount(repo.file_count) },
    { label: "Size", value: formatSize(repo.total_size) },
    { label: "Last publish", value: repo.last_published_at ? formatRelative(repo.last_published_at) : "Never" },
    ...Object.entries(status.data ?? {}).map(([k, v]) => ({ label: factLabel(k), value: factValue(v) })),
  ]);

  async function save() {
    const saved = await api.repos.update(repo.name, { description: draft.value.description.trim() });
    draft.commit({ description: saved.description });
    toast.success(`Saved the description of ${repo.name}`);
    onChanged();
  }
</script>

<FormSection title="Repository">
  <FormGrid>
    <FormRow label="Description" for="{id}-desc" help="The list of repositories shows this text after the name." full>
      <Textarea id="{id}-desc" rows={3} maxlength={max} aria-describedby="{id}-desc-help {id}-desc-count" bind:value={draft.value.description} />
      <p id="{id}-desc-count" class="text-right text-meta text-muted-foreground tabular-nums">{formatCount(draft.value.description.length)} of {max}</p>
    </FormRow>
  </FormGrid>
</FormSection>

<Section title="Contents">
  <dl class="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
    {#each facts as f (f.label)}
      <div class="min-w-0">
        <dt class="text-meta text-muted-foreground">{f.label}</dt>
        <dd class="truncate text-label tabular-nums" title={f.value}>{f.value}</dd>
      </div>
    {/each}
  </dl>
  {#if status.error}<div class="mt-4"><ErrorState inline error="The format's facts did not load: {status.error}" /></div>{/if}
</Section>

<SaveBar count={draft.count} onDiscard={draft.reset} onSave={save} />