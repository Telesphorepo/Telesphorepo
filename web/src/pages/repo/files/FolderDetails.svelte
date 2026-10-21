<!--
  The body of the details column at a plain folder. At the top folder: the facts of the whole
  repository (files, size, last publish). At any other folder: what it holds (its folders and its
  files, without checksum and signature files, as the views count them) and when a file in it was
  last published. The parent keys it by folder, and again after a delete. A group's top folder
  names its members in read order.
-->
<script lang="ts">
  import { api, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import { sideOf } from "../browse";
  import { formatCount } from "$lib/format";
  import { href, repoPath } from "$lib/router.svelte";

  let { repo, folder }: { repo: Repo; folder: string } = $props();

  const LIMIT = 100;

  // The top folder: the repository again, so the numbers are fresh after a delete.
  /* svelte-ignore state_referenced_locally */
  const top = folder === "" ? new Resource(() => api.repos.get(repo.name)) : null;

  // Any other folder: the first page of its children.
  /* svelte-ignore state_referenced_locally */
  const kids = folder === "" ? null : new Resource(() => api.repos.tree(repo.name, folder, { limit: LIMIT }));
  const held = $derived.by(() => {
    const page = kids?.data;
    if (!page) return null;
    const paths = new Set(page.items.map((e) => e.path));
    const shown = page.items.filter((e) => {
      const of = e.type === "file" ? sideOf(e.path) : null;
      return !of || !paths.has(of);
    });
    const files = shown.filter((e) => e.type === "file");
    const last = files.reduce<string>((m, f) => (f.updated_at && f.updated_at > m ? f.updated_at : m), "");
    return { folders: shown.length - files.length, files: files.length, last, more: page.next !== null };
  });

  const plural = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;
  const r = $derived(top?.data ?? repo);
</script>

{#if top && repo.kind === "group"}
  <!-- A group's top folder: the repositories it reads, in read order. -->
  <dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
    <dt class="text-muted-foreground">Reads from</dt>
    <dd class="min-w-0">
      {#if r.members?.length}
        <ol class="flex flex-col gap-1">
          {#each r.members as m (m.name)}
            <li class="truncate"><a class="text-link hover:underline" href={href(repoPath(m.name))}>{m.name}</a></li>
          {/each}
        </ol>
      {:else}
        <span class="text-muted-foreground">No repositories</span>
      {/if}
    </dd>
  </dl>
{:else if top}
  <dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
    <dt class="text-muted-foreground">Files</dt>
    <dd class="tabular-nums">{formatCount(r.file_count)}</dd>
    <dt class="text-muted-foreground">Size</dt>
    <dd class="tabular-nums"><FileSize bytes={r.total_size} /></dd>
    <dt class="text-muted-foreground">Last publish</dt>
    <dd>{#if r.last_published_at}<RelativeTime at={r.last_published_at} />{:else}<span class="text-muted-foreground">None yet</span>{/if}</dd>
  </dl>
{:else if kids}
  {#if kids.loading && !kids.data}
    <Loading rows={2} />
  {:else if kids.error && !kids.data}
    <ErrorState inline error={kids.error} />
  {:else if held}
    <dl class="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-4 gap-y-2 text-body">
      <dt class="text-muted-foreground">Holds</dt>
      <dd>
        {#if held.more}
          More than {formatCount(LIMIT)} items
        {:else if !held.folders && !held.files}
          <span class="text-muted-foreground">Nothing</span>
        {:else}
          {[held.folders && plural(held.folders, "folder", "folders"), held.files && plural(held.files, "file", "files")].filter(Boolean).join(", ")}
        {/if}
      </dd>
      {#if held.last && !held.more}
        <dt class="text-muted-foreground">Last publish</dt>
        <dd><RelativeTime at={held.last} /></dd>
      {/if}
    </dl>
  {/if}
{/if}
