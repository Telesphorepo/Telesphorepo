<!--
  The repositories of Browse home, for a visitor who comes to find one repository and use a package
  from it. One card holds a calm list; each repository is one row with the same fields in the same
  places, whatever its kind: the format icon, the name (prominent) with a lock after a private one,
  the description on one line under it, and at the end the format and the kind in plain words
  ("Maven", "Group of 3 repositories"). A row with no description keeps the height of the others.
  Counts and sizes are for the people who manage repositories: Settings › Repositories (RepoTable)
  and the details column of a repository's top folder show them. Under 640px the format and the
  kind move under the description. The whole row is the link.

    <RepoList repos={shown} href={(r) => href(repoPath(r.name))} />
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import { formats } from "$lib/formats/registry.svelte";
  import { kindPhrase } from "$lib/words";
  import FormatIcon from "./FormatIcon.svelte";
  import LockIcon from "@lucide/svelte/icons/lock";

  let { repos, href }: { repos: Repo[]; href: (r: Repo) => string } = $props();

  formats.load();
</script>

<ul aria-label="Repositories" class="divide-y overflow-hidden rounded-lg bg-card text-card-foreground ring-1 ring-foreground/10">
  {#each repos as r (r.name)}
    <li>
      <a
        href={href(r)}
        class="flex items-center gap-4 px-5 py-4 outline-none transition-colors hover:bg-hover focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:ring-inset sm:gap-5 sm:px-6"
      >
        <FormatIcon format={r.format} class="size-6 self-start text-muted-foreground max-sm:mt-0.5 sm:self-center" />
        <span class="flex min-w-0 flex-1 flex-col sm:min-h-12 sm:justify-center">
          <span class="flex min-w-0 items-center gap-2">
            <span class="truncate text-section">{r.name}</span>
            {#if !r.anonymous_read}
              <span class="flex shrink-0 text-muted-foreground" title="Private: only people with access can download">
                <LockIcon aria-hidden="true" class="size-4" /><span class="sr-only">Private</span>
              </span>
            {/if}
          </span>
          {#if r.description}<span class="truncate text-body text-muted-foreground" title={r.description}>{r.description}</span>{/if}
          <span class="mt-1 text-meta text-muted-foreground sm:hidden">{formats.title(r.format)} · {kindPhrase(r)}</span>
        </span>
        <span class="hidden shrink-0 flex-col items-end text-right text-meta text-muted-foreground sm:flex">
          <span>{formats.title(r.format)}</span>
          <span>{kindPhrase(r)}</span>
        </span>
      </a>
    </li>
  {/each}
</ul>
