<!--
  The body of the details column at an artifact or a version: what the artifact is (its kind and
  coordinates or package name), the version's facts and the GitHub Actions build it came from, then how
  to use it in a build, pinned to this version or always the newest.
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import CopyButton from "$lib/components/CopyButton.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ProvenanceFacts from "./ProvenanceFacts.svelte";
  import ArtifactSnippets from "../snippets/ArtifactSnippets.svelte";
  import { artifactKindLabel, buildInline, type Artifact, type Version } from "../browse";
  import { formatCount } from "$lib/format";

  let { repo, artifact, version }: { repo: Repo; artifact: Artifact; version: Version | null } = $props();

  const nameLabel = $derived(artifact.kind === "gradle-plugin" ? "Plugin ID" : artifact.kind === "package" ? "Package" : "Coordinates");
</script>

<dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
  <dt class="text-muted-foreground">Kind</dt>
  <dd class="flex min-w-0 items-center gap-1.5">
    <FormatIcon format={repo.format} kind={artifact.kind} />{artifactKindLabel[artifact.kind] ?? artifact.kind}
  </dd>
  <dt class="text-muted-foreground">{nameLabel}</dt>
  <dd class="flex min-w-0 items-start gap-1">
    <!-- Coordinates break after a dot or the colon, never inside a name. -->
    <span class="min-w-0 flex-1 pt-0.5 [overflow-wrap:anywhere]"
      >{#each artifact.title.split(/(?<=[.:])/) as part, i (i)}{part}<wbr />{/each}</span
    >
    <CopyButton value={artifact.title} label="Copy the {nameLabel === 'Package' ? 'package name' : nameLabel === 'Plugin ID' ? 'plugin ID' : 'coordinates'}" size="icon-xs" class="mt-px text-muted-foreground" />
  </dd>
  {#if version}
    <dt class="text-muted-foreground">Version</dt>
    <dd class="truncate">{version.version}</dd>
    <dt class="text-muted-foreground">Published</dt>
    <dd><RelativeTime at={version.published_at} /></dd>
    {#if version.kind === "snapshot"}
      <dt class="text-muted-foreground">Builds</dt>
      <dd>
        {formatCount(version.builds_count)}{#if version.latest_build}<span class="text-muted-foreground"> · latest is {buildInline(version.latest_build)}</span>{/if}
      </dd>
    {/if}
  {/if}
</dl>

{#if version?.provenance}
  <section class="flex flex-col gap-3" aria-label="Build">
    <h3 class="text-section">{version.kind === "snapshot" ? "Latest build" : "Build"}</h3>
    <ProvenanceFacts p={version.provenance} />
  </section>
{/if}

{#key artifact.path + "\n" + (version?.version ?? "")}<ArtifactSnippets {repo} path={artifact.path} version={version?.version ?? ""} />{/key}
