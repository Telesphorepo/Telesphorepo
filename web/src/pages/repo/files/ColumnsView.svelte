<!--
  Columns view (Miller columns), built for deep Maven trees: one column per folder from the top to
  the open folder, so every level stays visible. The open item of each column is filled; the
  deepest one has the selected fill. A click opens a folder in the next column or shows a file's
  details. Arrow keys move: up and down in a column, right into a folder, left back out. A version
  folder lists only its latest build; its earlier builds open in the List view. The box scrolls
  sideways inside itself, never the page.
-->
<script lang="ts">
  import { tick } from "svelte";
  import type { Repo } from "$lib/api";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import { viewBox } from "./area";
  import { Column, type Entry } from "./columns.svelte";
  import { baseName, parentOf } from "../browse";
  import { cn } from "$lib/utils";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import FileIcon from "@lucide/svelte/icons/file";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import HistoryIcon from "@lucide/svelte/icons/history";

  let {
    repo,
    folder,
    file,
    onEarlier,
  }: {
    repo: Repo;
    /** The open folder: "" or ending with "/". */
    folder: string;
    /** The selected file, or "". */
    file: string;
    /** Opens a version's earlier builds (in the List view). */
    onEarlier: (versionFolder: string) => void;
  } = $props();

  // The folders from the top down to the open one; each is a column.
  const folders = $derived.by(() => {
    const segs = folder.split("/").filter(Boolean);
    return ["", ...segs.map((_, i) => segs.slice(0, i + 1).join("/") + "/")];
  });

  // Columns stay loaded while the reader moves around in them.
  const cache = new Map<string, Column>();
  let columns = $state<Column[]>([]);
  $effect.pre(() => {
    const fs = folders;
    columns = fs.map((f) => {
      let c = cache.get(f);
      if (!c) {
        c = new Column(repo.name, f);
        cache.set(f, c);
      }
      return c;
    });
  });

  // The item each column has open: the next folder down, or the selected file in the last one.
  const openIn = (i: number) => folders[i + 1] ?? file;
  // The deepest open item, which the keys move from.
  const active = $derived(file || (folders.length > 1 ? folder : ""));

  let box = $state<HTMLDivElement | null>(null);

  // A new column, and a change of the box's width (the details column opens or closes), scroll the
  // box to its end, so the deepest columns stay in view. Going up, the content gets narrower and
  // the browser keeps its end in view by itself.
  function reveal(node: HTMLElement) {
    requestAnimationFrame(() => {
      const b = node.parentElement;
      if (b) b.scrollLeft = b.scrollWidth;
    });
  }
  function pinEnd(node: HTMLElement) {
    const ro = new ResizeObserver(() => (node.scrollLeft = node.scrollWidth));
    ro.observe(node);
    return { destroy: () => ro.disconnect() };
  }

  // Keep the open item of each column in view.
  $effect(() => {
    void columns.length;
    void active;
    tick().then(() => {
      if (!box) return;
      // Scroll each column by itself; scrollIntoView would also move the page.
      box.querySelectorAll<HTMLElement>("[data-open]").forEach((el) => {
        const col = el.parentElement;
        if (!col) return;
        const top = el.getBoundingClientRect().top - col.getBoundingClientRect().top + col.scrollTop;
        if (top < col.scrollTop) col.scrollTop = top - 4;
        else if (top + el.offsetHeight > col.scrollTop + col.clientHeight) col.scrollTop = top + el.offsetHeight - col.clientHeight + 4;
      });
    });
  });

  function openPath(path: string, replace = false) {
    navigate(filesPath(repo.name, path), { replace });
  }

  async function focusActive() {
    await tick();
    box?.querySelector<HTMLElement>(`[data-path="${CSS.escape(active)}"]`)?.focus();
  }

  function onkeydown(e: KeyboardEvent) {
    if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"].includes(e.key)) return;
    e.preventDefault();
    const isDir = active === "" || active.endsWith("/");
    if (e.key === "ArrowRight") {
      // Into the open folder: its first item.
      const first = isDir ? columns[folders.indexOf(active)]?.entries[0] : undefined;
      if (first) openPath(first.path, true);
    } else if (e.key === "ArrowLeft") {
      // Back out to the folder that holds the active item.
      if (active !== "" && parentOf(active) !== "") openPath(parentOf(active), true);
    } else {
      // Up and down in the column that holds the active item.
      const list = columns[active === "" ? 0 : folders.indexOf(parentOf(active))]?.entries ?? [];
      const i = list.findIndex((x) => x.path === active);
      const next = i < 0 ? list[0] : list[i + (e.key === "ArrowDown" ? 1 : -1)];
      if (next) openPath(next.path, true);
    }
    focusActive();
  }

  // Loads the next page when the end of a column comes into view.
  function sentinel(node: HTMLElement, c: Column) {
    const io = new IntersectionObserver((es) => {
      if (es.some((x) => x.isIntersecting) && c.hasMore && !c.loadingMore) c.more();
    });
    io.observe(node);
    return { destroy: () => io.disconnect() };
  }

  const columnName = (f: string) => (f === "" ? repo.name : baseName(f));
  // A long file name keeps its end ("…-8-sources.jar"): the files of one build differ only there.
  const short = (e: Entry) => (e.type === "file" && e.name.length > 26 ? e.name.slice(0, 9) + "…" + e.name.slice(-15) : e.name);
</script>

{#snippet item(c: Column, e: Entry, i: number)}
  {@const open = openIn(i) === e.path}
  {@const deepest = e.path === active}
  <a
    href={href(filesPath(repo.name, e.path))}
    data-path={e.path}
    data-open={open ? "" : undefined}
    aria-current={deepest ? "true" : undefined}
    tabindex={deepest || (active === "" && i === 0 && c.entries[0] === e) ? 0 : -1}
    class={cn(
      "flex h-8 shrink-0 items-center gap-2 rounded-lg px-2 text-body outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
      deepest ? "bg-selected text-selected-foreground" : open ? "bg-muted" : "hover:bg-hover",
    )}
  >
    {#if e.type === "dir"}
      <FolderIcon aria-hidden="true" class={cn("size-4 shrink-0", deepest ? "" : "text-link")} />
    {:else}
      <FileIcon aria-hidden="true" class={cn("size-4 shrink-0", deepest ? "" : "text-muted-foreground")} />
    {/if}
    <span class="min-w-0 flex-1 truncate" title={e.name.length > 26 ? e.name : undefined}>{short(e)}</span>
    {#if e.type === "dir"}<ChevronRightIcon aria-hidden="true" class="size-4 shrink-0 opacity-60" />{/if}
  </a>
{/snippet}

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  bind:this={box}
  use:pinEnd
  role="group"
  aria-label="Folders of {repo.name}, in columns"
  {onkeydown}
  class={cn("flex divide-x overflow-x-auto overflow-y-hidden rounded-lg ring-1 ring-border", viewBox)}
>
  {#each columns as c, i (c.folder)}
    <div use:reveal role="group" aria-label={columnName(c.folder)} class="flex w-60 shrink-0 flex-col overflow-y-auto p-1 max-sm:w-[calc((100vw-2.875rem)/2)]">
      {#if c.loading}
        <Loading inline class="min-h-8 px-2" />
      {:else if c.error}
        <div class="flex flex-col items-start gap-2 p-2">
          <ErrorState inline error={c.error} />
          <Button variant="outline" size="sm" onclick={c.reload}>Try again</Button>
        </div>
      {:else if c.entries.length === 0}
        <p class="p-2 text-meta text-muted-foreground">Empty folder</p>
      {:else}
        {#each c.entries as e (e.path)}{@render item(c, e, i)}{/each}
        {#if c.earlier}
          <button
            type="button"
            onclick={() => onEarlier(c.folder)}
            class="mt-1 flex h-8 shrink-0 items-center gap-2 rounded-lg px-2 text-left text-body text-muted-foreground outline-none hover:bg-hover hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            <HistoryIcon aria-hidden="true" class="size-4 shrink-0" />
            <span class="truncate">{c.earlier} earlier {c.earlier === 1 ? "build" : "builds"}</span>
          </button>
        {/if}
        {#if c.hasMore}
          <div use:sentinel={c} class="flex h-8 shrink-0 items-center gap-2 px-2 text-meta text-muted-foreground">
            {#if c.loadingMore}<Spinner />{/if}More
          </div>
        {/if}
      {/if}
    </div>
  {/each}
</div>
