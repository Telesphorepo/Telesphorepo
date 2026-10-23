<!--
  A repository as a card (Home, Browse, Publish), the same fields in the same places whatever its
  kind: the format icon, the name with a lock after a private one, the description up to two lines,
  and at the bottom the kind of a proxy or a group in plain words ("Group of 3 repositories"). The whole card is
  the link. No counts, sizes or times: Settings › Repositories (RepoTable) and the details of a
  repository's top folder show them.

    <CardGrid label="Repositories" max={3}>
      {#each shown as r (r.name)}<RepoCard repo={r} href={href(repoPath(r.name))} />{/each}
    </CardGrid>
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import { kindPhrase } from "$lib/words";
  import FormatIcon from "./FormatIcon.svelte";
  import ItemCard from "./ItemCard.svelte";
  import LockIcon from "@lucide/svelte/icons/lock";

  let { repo, href }: { repo: Repo; href: string } = $props();
</script>

{#snippet kind()}<span>{kindPhrase(repo)}</span>{/snippet}

<!-- A repository of your own is the default kind and says nothing; a proxy or a group says what it is. -->
<ItemCard title={repo.name} {href} titleClass="text-section" actions={repo.kind === "hosted" ? undefined : kind}>
  {#snippet icon()}<FormatIcon format={repo.format} class="size-6" />{/snippet}
  {#snippet badges()}
    {#if !repo.anonymous_read}
      <span class="flex text-muted-foreground" title="Private: only people with access can download">
        <LockIcon aria-hidden="true" class="size-4" /><span class="sr-only">Private</span>
      </span>
    {/if}
  {/snippet}
  {#if repo.description}<p class="line-clamp-2 text-body" title={repo.description}>{repo.description}</p>{/if}
</ItemCard>
