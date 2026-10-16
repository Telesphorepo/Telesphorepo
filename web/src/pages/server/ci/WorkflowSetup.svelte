<!--
  What the CI job needs, next to the pipeline that lets it publish: the GitHub Actions workflow or
  the Jenkinsfile for one of the chosen repositories (GET /api/repos/{name}/ci). The server builds
  the file for the build tool of the repository's format and the branches and tags of the
  pipeline, so the file and the pipeline agree. A GitHub workflow gets the workflow file of the
  pipeline as its name.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { api, type Repo, type Snippet } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import * as Select from "$lib/components/ui/select";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import Segmented from "../../repo/snippets/Segmented.svelte";
  import { workflowFile, type GitHubJobs } from "./presets";

  let { system, repos, github }: { system: "github" | "jenkins"; repos: string[]; github: GitHubJobs } = $props();

  const fid = $props.id();

  let picked = $state("");
  const repo = $derived(repos.includes(picked) ? picked : (repos[0] ?? ""));
  let tool = $state("");

  // The repository and its CI file, loaded together when the repository, the tool or the trigger changes.
  const file = new Resource<{ repo: Repo; snippet: Snippet } | null>(
    async () => {
      if (!repo) return null;
      const r = await api.repos.get(repo);
      const tools = formats.get(r.format)?.tools ?? [];
      const t = tools.some((x) => x.id === tool) ? tool : tools[0]?.id;
      const snippet = await api.repos.ci(repo, {
        system: system === "github" ? "github-actions" : "jenkins",
        tool: t,
        branches: system === "github" ? github.branches : [],
        tags: system === "github" ? github.tags : [],
      });
      return { repo: r, snippet };
    },
    { lazy: true },
  );
  const key = $derived(JSON.stringify([repo, tool, system, github.branches, github.tags]));
  let loaded = $state("");
  $effect(() => {
    const k = key;
    if (k !== loaded) {
      loaded = k;
      untrack(() => file.reload());
    }
  });

  const data = $derived(file.data);
  const toolChoices = $derived((data ? (formats.get(data.repo.format)?.tools ?? []) : []).map((t) => ({ value: t.id, label: t.label })));
  const chosenTool = $derived(toolChoices.some((t) => t.value === tool) ? tool : (toolChoices[0]?.value ?? ""));
  const title = $derived.by(() => {
    const s = data?.snippet;
    if (!s) return "";
    const name = s.file ?? s.title;
    const wf = workflowFile(github.workflow);
    return system === "github" && wf ? name.replace(/[^/]+$/, wf) : name;
  });
</script>

{#if repos.length === 0}
  <p class="text-meta text-muted-foreground">Choose a repository above. The steps depend on its format.</p>
{:else}
  <div class="flex flex-col gap-6">
    {#if repos.length > 1 || toolChoices.length > 1}
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
        {#if toolChoices.length > 1}
          <FormRow label="Build tool" help="The job publishes with the tool that builds the project.">
            <Segmented bind:value={() => chosenTool, (v) => (tool = v)} options={toolChoices} label="Build tool" />
          </FormRow>
        {/if}
      </FormGrid>
    {/if}

    {#if file.loading && !data}
      <Loading rows={6} />
    {:else if file.error && !data}
      <ErrorState error={file.error} title="The steps did not load" onRetry={file.reload} />
    {:else if data}
      <div class="flex min-w-0 flex-col gap-2">
        <CodeBlock {title} code={data.snippet.code} />
        {#if data.snippet.note}
          <p class="text-meta text-muted-foreground">{data.snippet.note}</p>
        {/if}
      </div>
    {/if}
  </div>
{/if}
