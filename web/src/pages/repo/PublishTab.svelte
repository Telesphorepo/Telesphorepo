<!--
  Publish to this repository, for the people who can publish: one card per place a publisher
  publishes from, and only the places this format has. From your computer (the publisher client of
  the Telesphorepo CLI: ProjectSetup makes a setup line, with the snippets behind "Set it up by
  hand"), from CI (the CI pipeline that a person with CI pipelines adds, then the CI file), and by
  hand (upload in the browser, or a publish token with curl). The snippets come from the server
  (GET /api/repos/{name}/usage). A pause shows once, as a line under the header (RepoView).
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import type { Resource } from "$lib/load.svelte";
  import { auth } from "$lib/auth.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Separator } from "$lib/components/ui/separator";
  import Section from "$lib/components/Section.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SnippetTabs from "./snippets/SnippetTabs.svelte";
  import ProjectSetup from "./ProjectSetup.svelte";
  import type { Usage } from "$lib/api";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import KeyRoundIcon from "@lucide/svelte/icons/key-round";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { repo, usage, canUpload, query }: { repo: Repo; usage: Resource<Usage>; canUpload: boolean; query: URLSearchParams } = $props();

  const publish = $derived((usage.data?.snippets ?? []).filter((s) => s.purpose === "publish"));
  const computer = $derived(publish.filter((s) => s.via === "computer"));
  const ci = $derived(publish.filter((s) => s.via === "ci"));
  const hand = $derived(publish.filter((s) => s.via === "hand"));
  const locked = $derived(!!repo.operation);
  const tools = $derived(usage.data?.tools ?? []);
</script>

{#snippet part(n: number, title: string, text?: string)}
  <div class="flex min-w-0 items-start gap-3">
    <span class="flex h-6 min-w-4 shrink-0 items-center text-label text-muted-foreground tabular-nums">{n}</span>
    <div class="min-w-0">
      <h3 class="text-label leading-6">{title}</h3>
      {#if text}<p class="max-w-prose text-meta text-muted-foreground">{text}</p>{/if}
    </div>
  </div>
{/snippet}

{#if usage.loading && !usage.data}
  <Loading rows={8} />
{:else if usage.error && !usage.data}
  <ErrorState error={usage.error} title="The instructions did not load" onRetry={usage.reload} />
{:else}
  <ProjectSetup {repo} baseURL={usage.data?.base_url ?? location.origin} {computer} {tools} {query} />

  {#if ci.length}
    <Section title="From CI" description="The job proves who it is with its own identity token, so it stores no secret.">
      <div class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        {@render part(1, "Add a CI pipeline", `It names the CI repository and branches that may publish to ${repo.name}.`)}
        {#if repo.permissions?.includes("ci.pipelines")}
          <Button variant="outline" class="shrink-0" href={href("/settings/ci-pipelines/new", { repo: repo.name, back: repoPath(repo.name, "publish") })}><PlusIcon />New CI pipeline</Button>
        {:else}
          <p class="text-meta text-muted-foreground sm:max-w-64 sm:text-right">Ask a person who manages the CI pipelines of this repository to add one for your CI repository and branch.</p>
        {/if}
      </div>
      <Separator class="my-6" />
      <div class="flex flex-col gap-4">
        {@render part(2, "Add the workflow")}
        <SnippetTabs snippets={ci} {tools} label="CI system" />
      </div>
    </Section>
  {/if}

  <!-- A publish token by hand needs a password account. -->
  {#if hand.length && auth.user?.kind === "local"}
    <Section title="By hand" description="Upload files in the browser, or send them with curl and a publish token.">
      {#snippet actions()}
        {#if canUpload}
          <Button variant="outline" href={href(repoPath(repo.name, "upload"))} disabled={locked}><UploadIcon />Upload files</Button>
        {/if}
        <Button variant="outline" href={href("/settings/tokens")}><KeyRoundIcon />Publish tokens</Button>
      {/snippet}
      <SnippetTabs snippets={hand} {tools} label="Tool" />
    </Section>
  {/if}
{/if}
