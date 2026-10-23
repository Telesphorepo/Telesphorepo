<!--
  Repository settings, Overview. Every kind has its description, saved from the save bar, and for
  a holder of All repositories its name: a rename asks first and says that the old name keeps
  working as a redirect. The old names show in their own card (RedirectsCard).
  A hosted repository: what it holds (files, size, last publish and the format's own facts) and the
  groups it is in.
  A proxy: its remote (address, last answer, "Test the remote"), how a build uses it, its cache
  (files and size) and the groups it is in.
  A group: how a build uses it ("Use this one address in your build") and its members in read
  order. The member edits go into the same draft; a save that removes members asks first.
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
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import UsageCard from "./UsageCard.svelte";
  import GroupsCard from "./GroupsCard.svelte";
  import GroupMembers from "./GroupMembers.svelte";
  import RemoteCard from "./RemoteCard.svelte";
  import RedirectsCard from "./RedirectsCard.svelte";
  import { Input } from "$lib/components/ui/input";
  import { auth } from "$lib/auth.svelte";
  import { navigate, repoSettingsPath } from "$lib/router.svelte";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const id = $props.id();
  const max = 500;
  const memberNames = (r: Repo) => (r.members ?? []).map((m) => m.name);
  const draft = new Draft({
    name: untrack(() => repo.name),
    description: untrack(() => repo.description),
    members: untrack(() => memberNames(repo)),
  });
  // A holder of All repositories renames the repository here; its old name stays as a redirect.
  const canRename = $derived(auth.can("repositories.all"));
  const newName = $derived(draft.value.name.trim());
  const renamed = $derived(newName !== draft.saved.name);
  const nameError = $derived(
    renamed && !/^[a-z0-9][a-z0-9._-]{0,63}$/.test(newName)
      ? "Use 1 to 64 lower-case letters, digits, '.', '_' or '-', starting with a letter or a digit."
      : "",
  );
  /* svelte-ignore state_referenced_locally */
  const status = repo.kind === "group" ? null : new Resource(() => api.repos.status(repo.name));

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
    { label: "Files", value: repo.counts_pending ? "Counting…" : formatCount(repo.file_count) },
    { label: "Size", value: repo.counts_pending ? "Counting…" : formatSize(repo.total_size) },
    ...(repo.kind !== "proxy" && !repo.counts_pending
      ? [{ label: "Last publish", value: repo.last_published_at ? formatRelative(repo.last_published_at) : "Never" }]
      : []),
    ...Object.entries(status?.data ?? {}).map(([k, v]) => ({ label: factLabel(k), value: factValue(v) })),
  ]);

  // The members that a save removes from the group.
  const removed = $derived(draft.saved.members.filter((m) => !draft.value.members.includes(m)));
  const membersChanged = $derived(draft.changed.includes("members"));
  let confirmOpen = $state(false);
  const changes = $derived.by(() => {
    const was = draft.saved.members;
    const now = draft.value.members;
    const kept = (list: string[]) => list.filter((m) => was.includes(m) && now.includes(m)).join("\n");
    return [
      ...(renamed
        ? [
            `Renames ${repo.name} to ${newName}`,
            `${repo.name} keeps working as a redirect: builds and projects that use it reach ${newName}, publishes included`,
            `Change them to ${newName} when you like; the redirect shows when it was last used`,
          ]
        : []),
      ...removed.map((m) => `Removes ${m}: builds that use ${repo.name} stop getting its files`),
      ...now.filter((m) => !was.includes(m)).map((m) => `Adds ${m}`),
      ...(kept(was) !== kept(now) ? ["Reads the members in the new order"] : []),
    ];
  });

  async function save() {
    if (nameError) throw new Error(nameError);
    if (removed.length || renamed) {
      confirmOpen = true;
      return;
    }
    await write();
  }

  async function write() {
    const v = draft.value;
    let description = draft.saved.description;
    let members = draft.saved.members;
    if (draft.changed.includes("description")) {
      description = (await api.repos.update(repo.name, { description: v.description.trim() })).description;
    }
    if (membersChanged) {
      members = memberNames(await api.repos.setMembers(repo.name, v.members));
    }
    let name = draft.saved.name;
    if (renamed) {
      name = (await api.repos.rename(repo.name, newName)).name;
    }
    draft.commit({ name, description, members });
    if (name !== repo.name) {
      toast.success(`Renamed ${repo.name} to ${name}`);
      navigate(repoSettingsPath(name, "overview"), { replace: true, force: true });
      return;
    }
    toast.success(`Saved ${repo.name}`);
    onChanged();
  }
</script>

<FormSection title={repo.kind === "proxy" ? "Proxy" : repo.kind === "group" ? "Group" : "Repository"}>
  <FormGrid>
    {#if canRename}
      <FormRow label="Name" for="{id}-name" help="A new name keeps the old one working as a redirect." error={nameError}>
        <Input id="{id}-name" bind:value={draft.value.name} autocomplete="off" spellcheck={false} aria-describedby="{id}-name-help" aria-invalid={nameError ? true : undefined} />
      </FormRow>
    {/if}
    <FormRow label="Description" for="{id}-desc" help="One sentence on what it holds, for people who look for it." full length={draft.value.description.length} {max}>
      <Textarea id="{id}-desc" rows={3} maxlength={max} aria-describedby="{id}-desc-help {id}-desc-count" bind:value={draft.value.description} />
    </FormRow>
  </FormGrid>
</FormSection>

{#if repo.kind === "group"}
  <UsageCard {repo} title="Use this one address in your build" />
  <GroupMembers {repo} bind:members={draft.value.members} />
{:else}
  {#if repo.kind === "proxy"}
    <RemoteCard {repo} {onChanged} />
    <UsageCard {repo} title="Use this proxy in your build" />
  {/if}

  <Section title={repo.kind === "proxy" ? "Cache" : "Contents"}>
    <dl class="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
      {#each facts as f (f.label)}
        <div class="min-w-0">
          <dt class="text-meta text-muted-foreground">{f.label}</dt>
          <dd class="truncate text-label tabular-nums" title={f.value}>{f.value}</dd>
        </div>
      {/each}
    </dl>
    {#if status?.error}<div class="mt-4"><ErrorState inline error="The format's facts did not load: {status.error}" /></div>{/if}
  </Section>

  {#if repo.groups?.length}
    <GroupsCard repo={repo.name} groups={repo.groups} />
  {/if}
{/if}

<RedirectsCard {repo} />

<SaveBar count={draft.count} onDiscard={draft.reset} onSave={save} />

<ConfirmDialog
  bind:open={confirmOpen}
  title={renamed ? `Rename ${repo.name} to ${newName}?` : `Save the members of ${repo.name}?`}
  confirmLabel={renamed ? "Rename" : "Save members"}
  destructive
  {changes}
  onConfirm={write}
/>
