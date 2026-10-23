<!--
  Publish to this repository, on a group: a group takes no publishes, so the person picks one of
  its repositories that they publish to (`targets`, in read order; the first by default), and
  the Publish tab of that repository follows. The choice stays in the address (?to=<repository>),
  so a link and the back button keep it.
-->
<script lang="ts">
  import { api, type GroupMember, type Repo } from "$lib/api";
  import { auth } from "$lib/auth.svelte";
  import { Resource } from "$lib/load.svelte";
  import { navigate, repoPath } from "$lib/router.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import * as Select from "$lib/components/ui/select";
  import Section from "$lib/components/Section.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import RepoKindIcon from "$lib/components/RepoKindIcon.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import PublishTab from "./PublishTab.svelte";

  let { group, targets, query }: { group: Repo; targets: GroupMember[]; query: URLSearchParams } = $props();

  const to = $derived(targets.find((m) => m.name === query.get("to"))?.name ?? targets[0].name);

  // The chosen repository and its instructions; a new choice loads both again.
  const target = $derived(new Resource<Repo>(() => api.repos.get(to)));
  const usage = $derived(new Resource(() => api.repos.usage(to)));
  const canUpload = $derived(
    !!target.data && !!formats.get(target.data.format)?.upload && auth.user?.kind === "local" && (target.data.permissions ?? []).includes("repo.publish"),
  );

  function choose(name: string) {
    if (name !== to) navigate(repoPath(group.name, "publish"), { query: { to: name }, replace: true });
  }
</script>

<Section title="Repository">
  <FormGrid>
    <FormRow label="Publish to" for="publish-to" help="A group serves the files of its repositories. Publish to one of them.">
      <Select.Root type="single" value={to} onValueChange={choose}>
        <Select.Trigger id="publish-to" class="w-full" aria-describedby="publish-to-help">
          {#snippet icon()}<FormatIcon format={group.format} />{/snippet}
          <span class="truncate">{to}</span>
        </Select.Trigger>
        <Select.Content>
          {#each targets as m (m.name)}
            <Select.Item value={m.name} label={m.name}>
              {#snippet icon()}<FormatIcon format={group.format} />{/snippet}
              <span class="truncate">{m.name}</span>
              <RepoKindIcon kind={m.kind} />
            </Select.Item>
          {/each}
        </Select.Content>
      </Select.Root>
    </FormRow>
  </FormGrid>
</Section>

{#if target.loading && !target.data}
  <Loading rows={8} />
{:else if target.error && !target.data}
  <ErrorState error={target.error} title="The repository did not load" onRetry={target.reload} />
{:else if target.data}
  {#key target.data.name}
    <PublishTab repo={target.data} {usage} {canUpload} {query} />
  {/key}
{/if}
