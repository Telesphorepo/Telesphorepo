<!--
  Tree view: the classic expandable tree of the whole repository, from the top folder down. A
  folder's children load the first time it opens (GET /tree, a page at a time), and they stay
  loaded while the reader moves around. The folders down to the open one start open. A version
  folder lists the files of its latest build, then a row "N earlier builds" that expands in place
  into the earlier builds, each of which expands into its files.

  A click on a folder opens it in the address and expands it; a click on the open folder, or on
  its arrow, folds it. A click on a file shows its details. Keys follow the tree pattern: up and
  down move, right expands or steps into a folder, left folds or steps out to the parent, Home and
  End jump, Enter opens. The box has the same size as the other views and scrolls inside itself.
-->
<script lang="ts">
  import { tick } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import type { Repo } from "$lib/api";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import FileSize from "$lib/components/FileSize.svelte";
  import MemberTag from "./MemberTag.svelte";
  import RowFileActions from "./RowFileActions.svelte";
  import { Column, type EarlierBuild, type Entry } from "./columns.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import { buildInline } from "../browse";
  import PackageIcon from "@lucide/svelte/icons/package";
  import { viewBox } from "./area";
  import { parentOf } from "../browse";
  import { cn } from "$lib/utils";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import FolderOpenIcon from "@lucide/svelte/icons/folder-open";
  import FileIcon from "@lucide/svelte/icons/file";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import HistoryIcon from "@lucide/svelte/icons/history";

  let {
    repo,
    folder,
    file,
    base = "",
  }: {
    repo: Repo;
    /** The public base URL, for Copy link. */
    base?: string;
    /** The open folder: "" or ending with "/". */
    folder: string;
    /** The selected file, or "". */
    file: string;
  } = $props();

  // The loaded folders, and the ones that are open. The top folder is always open.
  const cols = new SvelteMap<string, Column>();
  const open = new SvelteSet<string>();

  function expand(f: string) {
    if (!cols.has(f)) cols.set(f, new Column(repo.name, f));
    open.add(f);
  }
  const fold = (f: string) => open.delete(f);

  // The folders down to the open one open with it; folders the reader opened stay open.
  $effect.pre(() => {
    const segs = folder.split("/").filter(Boolean);
    expand("");
    segs.forEach((_, i) => expand(segs.slice(0, i + 1).join("/") + "/"));
  });

  // The selected item: the file, else the open folder.
  const selected = $derived(file || folder);

  type Row =
    | { kind: "entry"; key: string; e: Entry; level: number; pos: number; size: number }
    | { kind: "earlier"; key: string; folder: string; level: number; count: number }
    | { kind: "build"; key: string; folder: string; level: number; b: EarlierBuild }
    | { kind: "earlier-loading" | "earlier-error" | "earlier-more"; key: string; folder: string; level: number }
    | { kind: "loading" | "error" | "empty" | "more"; key: string; folder: string; level: number };

  // The visible rows, in reading order.
  const rows = $derived.by(() => {
    const out: Row[] = [];
    const walk = (f: string, level: number) => {
      const c = cols.get(f);
      if (!c) return;
      const es = c.entries;
      if (c.loading && !es.length) return void out.push({ kind: "loading", key: `loading:${f}`, folder: f, level });
      if (c.error && !es.length) return void out.push({ kind: "error", key: `error:${f}`, folder: f, level });
      if (!es.length) return void out.push({ kind: "empty", key: `empty:${f}`, folder: f, level });
      es.forEach((e, i) => {
        out.push({ kind: "entry", key: e.path, e, level, pos: i + 1, size: es.length });
        if (e.type === "dir" && open.has(e.path)) walk(e.path, level + 1);
      });
      if (c.earlier) {
        const ek = `earlier:${f}`;
        out.push({ kind: "earlier", key: ek, folder: f, level, count: c.earlier });
        if (open.has(ek)) {
          const bs = c.earlierBuilds;
          if (c.earlierLoading && !bs.length) out.push({ kind: "earlier-loading", key: `${ek}:loading`, folder: f, level: level + 1 });
          else if (c.earlierError && !bs.length) out.push({ kind: "earlier-error", key: `${ek}:error`, folder: f, level: level + 1 });
          bs.forEach((b) => {
            out.push({ kind: "build", key: b.key, folder: f, level: level + 1, b });
            if (open.has(b.key))
              b.files.forEach((e, i) => out.push({ kind: "entry", key: e.path, e, level: level + 2, pos: i + 1, size: b.files.length }));
          });
          if (c.earlierHasMore) out.push({ kind: "earlier-more", key: `${ek}:more`, folder: f, level: level + 1 });
        }
      }
      if (c.hasMore) out.push({ kind: "more", key: `more:${f}`, folder: f, level });
    };
    walk("", 1);
    return out;
  });

  // The rows the keys move through.
  const stops = $derived(rows.filter((r) => r.kind === "entry" || r.kind === "earlier" || r.kind === "build"));

  // The earlier builds of a version, and each build, fold and expand like folders.
  function toggleEarlier(f: string) {
    const k = `earlier:${f}`;
    if (open.has(k)) open.delete(k);
    else {
      cols.get(f)?.loadEarlier();
      open.add(k);
    }
  }
  const toggle = (k: string) => (open.has(k) ? open.delete(k) : open.add(k));

  // The row that holds the keyboard focus (roving tabindex). It follows the selection.
  let focused = $state("");
  $effect.pre(() => {
    focused = selected;
  });
  const tabStop = $derived(stops.some((r) => r.key === focused) ? focused : (stops[0]?.key ?? ""));

  let box = $state<HTMLDivElement | null>(null);

  // Keep a row in view by scrolling the box only; scrollIntoView would also move the page.
  function reveal(key: string) {
    const el = box?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`);
    if (!box || !el) return;
    const top = el.getBoundingClientRect().top - box.getBoundingClientRect().top + box.scrollTop;
    if (top < box.scrollTop) box.scrollTop = top - 4;
    else if (top + el.offsetHeight > box.scrollTop + box.clientHeight) box.scrollTop = top + el.offsetHeight - box.clientHeight + 4;
  }

  // The selected row comes into view once its folder has loaded, once per selection: a page that
  // loads below while the reader scrolls leaves the box where it is.
  let revealed = "";
  $effect(() => {
    const key = selected;
    if (key === revealed || !rows.some((r) => r.key === key)) return;
    revealed = key;
    tick().then(() => reveal(key));
  });

  async function moveTo(key: string) {
    focused = key;
    await tick();
    reveal(key);
    box?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)?.focus();
  }

  function activate(r: Row) {
    if (r.kind === "earlier") toggleEarlier(r.folder);
    else if (r.kind === "build") toggle(r.key);
    else if (r.kind === "entry") {
      if (r.e.type === "dir") expand(r.e.path);
      navigate(filesPath(repo.name, r.e.path));
    }
  }

  function onkeydown(ev: KeyboardEvent) {
    const i = stops.findIndex((r) => r.key === tabStop);
    const r = stops[i];
    if (!r) return;
    const dir = r.kind === "entry" && r.e.type === "dir" ? r.e.path : r.kind === "earlier" || r.kind === "build" ? r.key : null;
    const go: Record<string, () => void> = {
      ArrowDown: () => stops[i + 1] && moveTo(stops[i + 1].key),
      ArrowUp: () => stops[i - 1] && moveTo(stops[i - 1].key),
      Home: () => stops[0] && moveTo(stops[0].key),
      End: () => stops.length && moveTo(stops[stops.length - 1].key),
      ArrowRight: () => {
        if (!dir) return;
        if (!open.has(dir)) {
          if (r.kind === "earlier") toggleEarlier(r.folder);
          else if (r.kind === "build") open.add(dir);
          else expand(dir);
        }
        else if (stops[i + 1] && stops[i + 1].level > r.level) moveTo(stops[i + 1].key);
      },
      ArrowLeft: () => {
        if (dir && open.has(dir)) return void fold(dir);
        // A file of an earlier build steps out to its build row.
        const inBuild = r.kind === "entry" ? rows.find((x) => x.kind === "build" && x.b.files.some((e) => e.path === r.key)) : undefined;
        const up = inBuild ? inBuild.key : r.kind === "entry" ? parentOf(r.e.path) : r.kind === "build" ? `earlier:${r.folder}` : r.folder;
        if (up !== "") moveTo(up);
      },
      Enter: () => activate(r),
      " ": () => activate(r),
    };
    const fn = go[ev.key];
    if (!fn) return;
    ev.preventDefault();
    fn();
  }

  // A click on the open folder folds it; any other folder opens (the link sets the address).
  function onRowClick(ev: MouseEvent, e: Entry) {
    if (e.type !== "dir" || ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.button !== 0) return;
    focused = e.path;
    if (e.path === selected && open.has(e.path)) {
      ev.preventDefault();
      fold(e.path);
    } else expand(e.path);
  }

  // The arrow folds or expands without opening the folder.
  function onArrowClick(ev: MouseEvent, e: Entry) {
    ev.preventDefault();
    ev.stopPropagation();
    focused = e.path;
    if (open.has(e.path)) fold(e.path);
    else expand(e.path);
  }

  // Loads the next page of a folder when its end comes into view.
  function sentinel(node: HTMLElement, f: string) {
    const io = new IntersectionObserver((es) => {
      const c = cols.get(f);
      if (es.some((x) => x.isIntersecting) && c?.hasMore && !c.loadingMore) c.more();
    });
    io.observe(node);
    return { destroy: () => io.disconnect() };
  }

  function earlierSentinel(node: HTMLElement, f: string) {
    const io = new IntersectionObserver((es) => {
      const c = cols.get(f);
      if (es.some((x) => x.isIntersecting) && c?.earlierHasMore && !c.earlierLoadingMore) c.moreEarlier();
    });
    io.observe(node);
    return { destroy: () => io.disconnect() };
  }

  const pad = (level: number) => `padding-left: ${0.25 + (level - 1) * 1.25}rem`;
  const rowClass = "flex h-8 shrink-0 items-center gap-1.5 rounded-lg pr-2 text-body outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50";
</script>

<!-- svelte-ignore a11y_interactive_supports_focus -->
<div
  bind:this={box}
  role="tree"
  aria-label="Files of {repo.name}"
  {onkeydown}
  class={cn("flex flex-col overflow-y-auto rounded-lg p-1 ring-1 ring-border", viewBox)}
>
  {#each rows as r (r.key)}
    {#if r.kind === "entry"}
      {@const e = r.e}
      {@const isDir = e.type === "dir"}
      {@const isOpen = isDir && open.has(e.path)}
      {@const on = e.path === selected}
      <div role="none" class="group/row relative flex flex-col">
      <a
        href={href(filesPath(repo.name, e.path))}
        role="treeitem"
        data-key={r.key}
        aria-level={r.level}
        aria-setsize={r.size}
        aria-posinset={r.pos}
        aria-expanded={isDir ? isOpen : undefined}
        aria-selected={on}
        tabindex={r.key === tabStop ? 0 : -1}
        onclick={(ev) => onRowClick(ev, e)}
        onfocus={() => (focused = r.key)}
        style={pad(r.level)}
        class={cn(rowClass, on ? "bg-selected text-selected-foreground" : "hover:bg-hover")}
      >
        {#if isDir}
          <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
          <span aria-hidden="true" onclick={(ev) => onArrowClick(ev, e)} class="-my-1 flex size-6 shrink-0 items-center justify-center rounded-md hover:bg-foreground/8">
            <ChevronRightIcon class={cn("size-4 opacity-60 transition-transform duration-150 motion-reduce:transition-none", isOpen && "rotate-90")} />
          </span>
          {#if isOpen}
            <FolderOpenIcon aria-hidden="true" class={cn("size-4 shrink-0", on ? "" : "text-link")} />
          {:else}
            <FolderIcon aria-hidden="true" class={cn("size-4 shrink-0", on ? "" : "text-link")} />
          {/if}
        {:else}
          <span aria-hidden="true" class="size-6 shrink-0"></span>
          <FileIcon aria-hidden="true" class={cn("size-4 shrink-0", on ? "" : "text-muted-foreground")} />
        {/if}
        <span class={cn("min-w-0 flex-1 truncate", isDir && "font-medium")} title={e.name.length > 40 ? e.name : undefined}>{e.name}</span>
        <MemberTag repo={e.repo} />
        {#if e.size !== undefined}
          <span class={cn("shrink-0 text-meta tabular-nums", isDir ? "" : "group-hover/row:invisible group-focus-within/row:invisible [@media(hover:none)]:hidden", on ? "" : "text-muted-foreground")}><FileSize bytes={e.size} /></span>
        {/if}
      </a>
      {#if !isDir && base}<RowFileActions {repo} path={e.path} {base} class="absolute top-1 right-1" />{/if}
      </div>
    {:else if r.kind === "earlier" || r.kind === "build"}
      {@const isOpen = open.has(r.key)}
      <button
        type="button"
        role="treeitem"
        aria-level={r.level}
        aria-selected="false"
        aria-expanded={isOpen}
        data-key={r.key}
        tabindex={r.key === tabStop ? 0 : -1}
        onclick={() => activate(r)}
        onfocus={() => (focused = r.key)}
        style={pad(r.level)}
        class={cn(rowClass, "text-left hover:bg-hover", r.kind === "earlier" && "text-muted-foreground hover:text-foreground")}
      >
        <span aria-hidden="true" class="-my-1 flex size-6 shrink-0 items-center justify-center">
          <ChevronRightIcon class={cn("size-4 opacity-60 transition-transform duration-150 motion-reduce:transition-none", isOpen && "rotate-90")} />
        </span>
        {#if r.kind === "earlier"}
          <HistoryIcon aria-hidden="true" class="size-4 shrink-0" />
          <span class="truncate">{r.count} earlier {r.count === 1 ? "build" : "builds"}</span>
        {:else}
          <PackageIcon aria-hidden="true" class="size-4 shrink-0 text-muted-foreground" />
          <span class="min-w-0 flex-1 truncate">{buildInline(r.b.id, r.b.label)}</span>
          {#if r.b.published_at}<span class="shrink-0 text-meta text-muted-foreground"><RelativeTime at={r.b.published_at} /></span>{/if}
        {/if}
      </button>
    {:else if r.kind === "earlier-loading"}
      <div role="none" style={pad(r.level)} class="flex items-center gap-1.5">
        <span class="size-6 shrink-0"></span><Loading inline class="min-h-8" />
      </div>
    {:else if r.kind === "earlier-error"}
      {@const c = cols.get(r.folder)}
      <div role="none" style={pad(r.level)} class="flex flex-wrap items-center gap-2 py-1">
        <span class="size-6 shrink-0"></span>
        <ErrorState inline error={c?.earlierError ?? ""} />
      </div>
    {:else if r.kind === "earlier-more"}
      {@const c = cols.get(r.folder)}
      <div role="none" use:earlierSentinel={r.folder} style={pad(r.level)} class="flex h-8 shrink-0 items-center gap-1.5 text-meta text-muted-foreground">
        <span class="size-6 shrink-0"></span>
        {#if c?.earlierLoadingMore}<Spinner class="size-4" />{/if}More
      </div>
    {:else if r.kind === "loading"}
      <div role="none" style={pad(r.level)} class="flex items-center gap-1.5">
        <span class="size-6 shrink-0"></span><Loading inline class="min-h-8" />
      </div>
    {:else if r.kind === "error"}
      {@const c = cols.get(r.folder)}
      <div role="none" style={pad(r.level)} class="flex flex-wrap items-center gap-2 py-1">
        <span class="size-6 shrink-0"></span>
        <ErrorState inline error={c?.error ?? ""} />
        <Button variant="outline" size="sm" onclick={() => c?.reload()}>Try again</Button>
      </div>
    {:else if r.kind === "empty"}
      <p role="none" style={pad(r.level)} class="flex h-8 shrink-0 items-center gap-1.5 text-meta text-muted-foreground">
        <span class="size-6 shrink-0"></span>Empty folder
      </p>
    {:else}
      {@const c = cols.get(r.folder)}
      <div role="none" use:sentinel={r.folder} style={pad(r.level)} class="flex h-8 shrink-0 items-center gap-1.5 text-meta text-muted-foreground">
        <span class="size-6 shrink-0"></span>
        {#if c?.loadingMore}<Spinner class="size-4" />{/if}More
      </div>
    {/if}
  {/each}
</div>
