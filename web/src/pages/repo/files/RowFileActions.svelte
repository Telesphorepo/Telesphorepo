<!--
  Download and Copy link at the end of a file's row, so a file is one click away without opening
  it. They show while the pointer is over the row (the parent row is a `group/row`) or a control
  has the focus, and always on a touch screen. Clicks do not reach the row's link.
-->
<script lang="ts">
  import { copyFileLink } from "./link";
  import { api, type Repo } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import { cn } from "$lib/utils";
  import { baseName } from "../browse";
  import DownloadIcon from "@lucide/svelte/icons/download";
  import LinkIcon from "@lucide/svelte/icons/link";

  let { repo, path, base, class: className = "" }: { repo: Repo; path: string; base: string; class?: string } = $props();

  const rel = $derived(api.repos.fileURL(repo.format, repo.name, path));

  function copy(ev: MouseEvent) {
    ev.preventDefault();
    ev.stopPropagation();
    copyFileLink(repo, path, base);
  }
</script>

<span
  class={cn(
    "flex shrink-0 items-center opacity-0 group-hover/row:opacity-100 focus-within:opacity-100 [@media(hover:none)]:opacity-100",
    className,
  )}
>
  <Button
    variant="ghost"
    size="icon-xs"
    href={rel}
    download={baseName(path)}
    aria-label="Download {baseName(path)}"
    title="Download"
    onclick={(ev: MouseEvent) => ev.stopPropagation()}><DownloadIcon /></Button
  >
  <Button variant="ghost" size="icon-xs" aria-label="Copy the link of {baseName(path)}" title="Copy link" onclick={copy}><LinkIcon /></Button>
</span>
