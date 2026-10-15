<!--
  What the CI job needs, next to the pipeline that lets it publish: the GitHub Actions workflow or
  the Jenkinsfile stage for one of the chosen repositories. The code comes from the repository's
  format plugin (GET /api/repos/{name}/usage, the CI publish snippets). A GitHub workflow gets the
  branches and tags of the pipeline as its trigger and the workflow file as its title, so the
  workflow and the pipeline agree.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { Resource } from "$lib/load.svelte";
  import * as Select from "$lib/components/ui/select";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import Segmented from "../../repo/snippets/Segmented.svelte";
  import { browse } from "../../repo/browse";
  import { workflowFile, type GitHubJobs } from "./presets";

  let { system, repos, github }: { system: "github" | "jenkins"; repos: string[]; github: GitHubJobs } = $props();

  const fid = $props.id();
  const client = $derived(system === "github" ? "github-actions" : "jenkins");

  let picked = $state("");
  const repo = $derived(repos.includes(picked) ? picked : (repos[0] ?? ""));
  const usage = new Resource(() => (repo ? browse.usage(repo) : Promise.resolve(null)), { lazy: true });
  let loaded = $state("");
  $effect(() => {
    const r = repo;
    if (r && r !== loaded) {
      loaded = r;
      untrack(() => usage.reload());
    }
  });
  const waiting = $derived(!!repo && repo !== loaded);

  const snippets = $derived((usage.data?.snippets ?? []).filter((s) => s.purpose === "publish" && s.via === "ci" && s.client === client));
  /** "Gradle" from "Jenkinsfile (Gradle)"; one snippet has no tool name. */
  const toolOf = (title: string) => /\(([^)]+)\)$/.exec(title)?.[1] ?? "";
  let tool = $state("");
  const chosen = $derived(snippets.find((s) => toolOf(s.title) === tool) ?? snippets[0]);
  const tools = $derived(snippets.map((s) => ({ value: toolOf(s.title), label: toolOf(s.title) })));

  const quote = (xs: string[]) => `[${xs.map((x) => JSON.stringify(x)).join(", ")}]`;
  function trigger(g: GitHubJobs): string {
    let s = "on:\n  push:\n";
    if (g.branches.length) s += `    branches: ${quote(g.branches)}\n`;
    if (g.tags.length) s += `    tags: ${quote(g.tags)}\n`;
    return s + "\n";
  }

  const code = $derived(chosen ? (system === "github" ? trigger(github) + chosen.code : chosen.code) : "");
  const title = $derived.by(() => {
    if (!chosen) return "";
    const t = chosen.title.replace(/\s*\([^)]+\)$/, "");
    const file = workflowFile(github.workflow);
    return system === "github" && file ? t.replace(/[^/]+$/, file) : t;
  });
</script>

{#if repos.length === 0}
  <p class="text-meta text-muted-foreground">Choose a repository above. The steps depend on its format.</p>
{:else}
  <div class="flex flex-col gap-6">
    {#if repos.length > 1 || snippets.length > 1}
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
        {#if snippets.length > 1}
          <FormRow label="Build tool" help="The job publishes with the tool that builds the project.">
            <Segmented bind:value={() => toolOf(chosen?.title ?? ""), (v) => (tool = v)} options={tools} label="Build tool" />
          </FormRow>
        {/if}
      </FormGrid>
    {/if}

    {#if (usage.loading || waiting) && !usage.data}
      <Loading rows={6} />
    {:else if usage.error && !usage.data}
      <ErrorState error={usage.error} title="The steps did not load" onRetry={usage.reload} />
    {:else if chosen}
      <div class="flex min-w-0 flex-col gap-2">
        <CodeBlock {title} {code} />
        {#if system === "jenkins"}
          <p class="text-meta text-muted-foreground">Jenkins makes the token with the OpenID Connect Provider plugin. {chosen.note ?? ""}</p>
        {:else if chosen.note}
          <p class="text-meta text-muted-foreground">{chosen.note}</p>
        {/if}
      </div>
    {:else}
      <p class="text-meta text-muted-foreground">The format of {repo} has no {system === "github" ? "GitHub Actions" : "Jenkins"} steps.</p>
    {/if}
  </div>
{/if}
