<!--
  Publish to this repository, for the people who can publish: one card per place a publisher
  publishes from, and only the places this format has. From your computer (the Gradle plugin or
  the Maven extension, with a one-time device approval: two-step verification for a password
  account, a new single sign-on for a single sign-on account), from CI (the CI pipeline that a person with CI pipelines
  adds, then the workflow), and by hand (upload in the browser, or a publish token with curl). The
  snippets come from the format plugin. A Maven repository shows one setup command in place of
  the computer and CI cards (ProjectSetup), with those snippets behind "Set it up by hand". A
  pause shows once, above the tabs (RepoView).
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
  import type { Usage } from "./browse";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import KeyRoundIcon from "@lucide/svelte/icons/key-round";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { repo, usage, canUpload }: { repo: Repo; usage: Resource<Usage>; canUpload: boolean } = $props();

  const publish = $derived((usage.data?.snippets ?? []).filter((s) => s.purpose === "publish"));
  const computer = $derived(publish.filter((s) => s.via === "computer"));
  const ci = $derived(publish.filter((s) => s.via === "ci"));
  const hand = $derived(publish.filter((s) => s.via === "hand"));
  const locked = $derived(!!repo.locked_by);
  // The setup command (GET /setup.sh, /setup.ps1) sets up Gradle and Maven projects.
  const setup = $derived(repo.format === "maven" && computer.length > 0);
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
  {#if setup}
    <ProjectSetup {repo} baseURL={usage.data?.base_url ?? location.origin} {computer} {ci} />
  {:else if computer.length}
    <Section
      title="From your computer"
      description={auth.user?.kind === "oidc"
        ? "The Gradle plugin or the Maven extension signs this computer in once, after you approve it with a new single sign-on."
        : "The Gradle plugin or the Maven extension signs this computer in once, after you approve it with two-step verification."}
    >
      <SnippetTabs snippets={computer} label="Build tool" />
    </Section>
  {/if}

  {#if ci.length && !setup}
    <Section title="From CI" description="The job proves who it is with its own identity token, so it stores no secret.">
      <div class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        {@render part(1, "Add a CI pipeline", `It names the CI repository and branches that may publish to ${repo.name}.`)}
        {#if repo.permissions?.includes("pipelines")}
          <Button variant="outline" class="shrink-0" href={href("/settings/ci-pipelines/new", { repo: repo.name, back: repoPath(repo.name, "publish") })}><PlusIcon />New CI pipeline</Button>
        {:else}
          <p class="text-meta text-muted-foreground sm:max-w-64 sm:text-right">Ask a person who manages the CI pipelines of this repository to add one for your CI repository and branch.</p>
        {/if}
      </div>
      <Separator class="my-6" />
      <div class="flex flex-col gap-4">
        {@render part(2, "Add the workflow")}
        <SnippetTabs snippets={ci} label="CI system" />
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
      <SnippetTabs snippets={hand} label="Tool" />
    </Section>
  {/if}
{/if}
