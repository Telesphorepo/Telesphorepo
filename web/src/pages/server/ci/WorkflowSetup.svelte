<!--
  What the CI job needs, next to the pipeline that lets it publish: the GitHub Actions workflow or
  the Jenkinsfile for one of the chosen repositories (GET /api/repos/{name}/ci), as the change to
  the reader's own file with the whole file one click away (SnippetBlock). The server builds the
  file for each build tool of the repository's format with the branches and tags of the pipeline,
  so the file and the pipeline agree. Tools that give the same file show as one ("Gradle and
  Maven"); the build tool choice shows only when the files differ. A GitHub workflow gets the
  workflow file of the pipeline as its name.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { api, type Snippet } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import * as Select from "$lib/components/ui/select";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import Segmented from "$lib/components/Segmented.svelte";
  import SnippetBlock, { andList, mergeSame } from "../../repo/snippets/SnippetBlock.svelte";
  import { workflowFile, type GitHubJobs } from "./presets";

  let { system, repos, github }: { system: "github" | "jenkins"; repos: string[]; github: GitHubJobs } = $props();

  const fid = $props.id();

  let picked = $state("");
  const repo = $derived(repos.includes(picked) ? picked : (repos[0] ?? ""));
  let chosen = $state("");

  // The CI file of every build tool of the repository, loaded together when the repository or the trigger changes.
  const files = new Resource<{ snippet: Snippet; titles: string[] }[] | null>(
    async () => {
      if (!repo) return null;
      const r = await api.repos.get(repo);
      const tools = formats.get(r.format)?.tools ?? [];
      const opts = {
        system: system === "github" ? ("github-actions" as const) : ("jenkins" as const),
        branches: system === "github" ? github.branches : [],
        tags: system === "github" ? github.tags : [],
      };
      const ids = tools.length ? tools.map((t) => t.id) : [undefined];
      return mergeSame(await Promise.all(ids.map((tool) => api.repos.ci(repo, { ...opts, tool }))));
    },
    { lazy: true },
  );
  const key = $derived(JSON.stringify([repo, system, github.branches, github.tags]));
  let loaded = $state("");
  $effect(() => {
    const k = key;
    if (k !== loaded) {
      loaded = k;
      untrack(() => files.reload());
    }
  });

  const groups = $derived(files.data ?? []);
  const choices = $derived(groups.map((g) => ({ value: g.snippet.id, label: andList(g.titles) })));
  const shown = $derived(groups.find((g) => g.snippet.id === chosen) ?? groups[0]);
</script>

{#if repos.length === 0}
  <p class="text-meta text-muted-foreground">Choose a repository above. The steps depend on its format.</p>
{:else}
  <div class="flex flex-col gap-6">
    {#if repos.length > 1 || choices.length > 1}
      <FormGrid>
        {#if repos.length > 1}
          <FormRow label="Repository" for="{fid}-repo" help="Each repository needs its own publish step.">
            <Select.Root type="single" value={repo} onValueChange={(v) => (picked = v)}>
              <Select.Trigger id="{fid}-repo" class="w-full" aria-describedby="{fid}-repo-help">{repo}</Select.Trigger>
              <Select.Content>
                {#each repos as r (r)}<Select.Item value={r} label={r}>{r}</Select.Item>{/each}
              </Select.Content>
            </Select.Root>
          </FormRow>
        {/if}
        {#if choices.length > 1}
          <FormRow label="Build tool" help="The job publishes with the tool that builds the project.">
            <Segmented bind:value={() => shown?.snippet.id ?? "", (v) => (chosen = v)} options={choices} label="Build tool" />
          </FormRow>
        {/if}
      </FormGrid>
    {/if}

    {#if files.loading && !files.data}
      <Loading rows={6} />
    {:else if files.error && !files.data}
      <ErrorState error={files.error} title="The steps did not load" onRetry={files.reload} />
    {:else if shown}
      <SnippetBlock snippet={shown.snippet} label={andList(shown.titles)} workflow={system === "github" ? workflowFile(github.workflow) : ""} />
    {/if}
  </div>
{/if}
