<!--
  Use this repository: what a developer needs on a first visit to add the repository to a build.
  The repository address, then the format plugin's snippets with a tab per client (Gradle Kotlin,
  Gradle Groovy, Maven; apt; curl and wget). A private repository adds a first step, the read
  token that the build signs in with, and its snippets carry the credentials; a public one has no
  such step. The header's lock already says which it is.
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import type { Resource } from "$lib/load.svelte";
  import { href } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import Section from "$lib/components/Section.svelte";
  import Step from "$lib/components/Step.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import SnippetTabs from "./snippets/SnippetTabs.svelte";
  import type { Usage } from "$lib/api";
  import KeyRoundIcon from "@lucide/svelte/icons/key-round";
  import TerminalIcon from "@lucide/svelte/icons/square-terminal";

  let { repo, usage, base }: { repo: Repo; usage: Resource<Usage>; base: string } = $props();

  const address = $derived(`${base}/${repo.format}/${repo.name}`);
  const use = $derived((usage.data?.snippets ?? []).filter((s) => s.purpose === "use"));
</script>

{#snippet build()}
  {#if usage.loading && !usage.data}
    <Loading rows={6} />
  {:else if usage.error && !usage.data}
    <ErrorState error={usage.error} title="The instructions did not load" onRetry={usage.reload} />
  {:else if use.length}
    <SnippetTabs snippets={use} tools={usage.data?.tools} label="Build tool" />
  {:else}
    <CompactEmpty icon={TerminalIcon} title="Instructions" state="None for this format" text="Download files from the repository address." />
  {/if}
{/snippet}

<Section title="Repository address">
  <CodeSnippet value={address} />
</Section>

{#if repo.anonymous_read}
  <Section title="Add it to your build">{@render build()}</Section>
{:else}
  <Step n={1} title="Create a read token">
    {#snippet actions()}
      <Button variant="outline" href={href("/settings/tokens")}><KeyRoundIcon />Create a read token</Button>
    {/snippet}
    <p class="text-body text-muted-foreground">Your build signs in with your username and a read token.</p>
  </Step>
  <Step n={2} title="Add it to your build">{@render build()}</Step>
{/if}
