<!--
  Upload files to a repo from Browse (spec §13): drop files, fill the format's fields, watch the
  progress, then see what the server stored. The page holds the publish session: token() gives
  its token or throws when it has ended, and onError hears every failed upload with the token it
  used, so the page can forget a session the server refused (401).

    <UploadDialog bind:open {repo} {folder} {token} onError={(e, t) => checkSession(e, t)}
      onUploaded={() => version++} onFolder={(d) => openFolderUpload(d)} />

  folder is the open folder ("" for the root, otherwise ending in "/"); Debian and raw use it for
  their first field values. Formats: maven, debian, raw (the ones with format.ManualUploader).
  A folder dropped here is not added: the dialog says to use "Upload a folder", and its button
  hands the dropped folder to onFolder.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import * as Dialog from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import { api, errorMessage, type Repo, type Uploaded, type UploadProgress } from "$lib/api";
  import { encodePath, navigate } from "$lib/router.svelte";
  import { formatSize } from "$lib/format";
  import DropZone from "./DropZone.svelte";
  import FileList from "./FileList.svelte";
  import MavenFields from "./MavenFields.svelte";
  import DebianFields from "./DebianFields.svelte";
  import RawFields from "./RawFields.svelte";
  import UploadResults from "./UploadResults.svelte";
  import type { UploadPlan } from "./types";
  import type { FolderDrop } from "./folder";
  import FolderUpIcon from "@lucide/svelte/icons/folder-up";

  let {
    open = $bindable(false),
    repo,
    folder = "",
    token,
    onError,
    onUploaded,
    onFolder,
  }: {
    open?: boolean;
    repo: Repo;
    folder?: string;
    /** The publish session token for repo; throws when the session has ended. */
    token: () => string;
    /** Hears a failed upload and the token it used. */
    onError?: (e: unknown, token: string | undefined) => void;
    /** Runs after a successful upload, e.g. to reload the tree. */
    onUploaded?: (files: Uploaded[]) => void;
    /** Opens "Upload a folder" with a folder that the user dropped here. */
    onFolder: (drop: FolderDrop) => void;
  } = $props();

  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;

  // The server takes at most this many files per upload (internal/api/files.go).
  const maxFiles = 500;

  const texts: Record<string, { description: string; hint: string; accept?: string }> = {
    maven: {
      description: "Upload a pom and its artifacts (jar, sources, javadoc), or a lone artifact with its coordinates.",
      hint: "A pom and files named <artifactId>-<version>[-<classifier>].<ext>",
    },
    debian: {
      description: "Upload .deb packages into a distribution and component.",
      hint: ".deb packages",
      accept: ".deb",
    },
    raw: {
      description: "Upload files into a folder of this repository.",
      hint: "Any files",
    },
  };
  const text = $derived(texts[repo.format] ?? texts.raw);

  let phase = $state<"form" | "sending" | "done">("form");
  let picked = $state<File[]>([]);
  let plan = $state<UploadPlan>();
  let error = $state("");
  let progress = $state<UploadProgress | null>(null);
  let results = $state<Uploaded[]>([]);
  let droppedFolder = $state<FolderDrop | null>(null);
  let abort: (() => void) | null = null;

  function reset() {
    phase = "form";
    picked = [];
    droppedFolder = null;
    plan = undefined;
    error = "";
    progress = null;
    results = [];
  }

  $effect(() => {
    if (open) untrack(reset);
  });

  function add(list: File[]) {
    error = "";
    droppedFolder = null;
    const byName = new Map(picked.map((f) => [f.name, f]));
    for (const f of list) byName.set(f.name, f);
    let all = [...byName.values()];
    if (all.length > maxFiles) {
      all = all.slice(0, maxFiles);
      error = `Upload at most ${maxFiles} files at a time. The rest were left out.`;
    }
    picked = all;
  }

  function remove(f: File) {
    picked = picked.filter((x) => x !== f);
    error = "";
  }

  const busy = $derived(phase === "sending");
  const totalBytes = $derived(picked.reduce((n, f) => n + f.size, 0));
  const blocked = $derived(!picked.length || !plan || plan.problem !== "");

  async function upload() {
    if (!plan || blocked) return;
    error = "";
    let t: string | undefined;
    try {
      t = token();
      phase = "sending";
      progress = { sent: 0, total: totalBytes };
      const form = new FormData();
      // Fields first: the server reads the parts in order.
      for (const [k, v] of Object.entries(plan.fields)) if (v) form.append(k, v);
      for (const f of picked) form.append("file", f, f.name);
      const s = api.repos.upload(repo.name, form, t, (p) => (progress = p));
      abort = s.abort;
      const answer = await s.done;
      results = answer.files;
      phase = "done";
      toast.success(`Uploaded ${nFiles(picked.length)} to ${repo.name}`);
      onUploaded?.(answer.files);
    } catch (e) {
      phase = "form";
      if (e instanceof DOMException && e.name === "AbortError") error = "You cancelled the upload. Nothing was stored.";
      else {
        error = errorMessage(e);
        onError?.(e, t);
      }
    } finally {
      abort = null;
      progress = null;
    }
  }

  // The folder that holds the first stored file, to open after the upload.
  const resultFolder = $derived.by(() => {
    const first = results.find((f) => !/\.(md5|sha1|sha256)$/.test(f.path));
    if (!first) return null;
    const i = first.path.lastIndexOf("/");
    return i < 0 ? "" : first.path.slice(0, i + 1);
  });

  function openFolder() {
    const f = resultFolder ?? "";
    open = false;
    navigate(`/repo/${encodeURIComponent(repo.name)}/${encodePath(f)}`);
  }

  const percent = $derived(progress && progress.total ? Math.min(100, Math.round((progress.sent / progress.total) * 100)) : 0);
  const storing = $derived(!!progress && progress.total > 0 && progress.sent >= progress.total);
</script>

<Dialog.Root bind:open={() => open, (v) => { if (!busy) open = v; }}>
  <Dialog.Content class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
    <Dialog.Header>
      <Dialog.Title>Upload to {repo.name}</Dialog.Title>
      <Dialog.Description>{text.description}</Dialog.Description>
    </Dialog.Header>

    {#if phase === "form"}
      <div class="flex flex-col gap-4">
        <DropZone onFiles={add} onFolder={(d) => (droppedFolder = d)} hint={text.hint} accept={text.accept} disabled={busy} />
        {#if droppedFolder}
          <div class="flex flex-wrap items-center justify-between gap-3 rounded-xl bg-muted/60 px-4 py-3 text-sm" role="alert">
            <p>That is a folder — use Upload a folder.</p>
            <Button
              size="sm"
              onclick={() => {
                const d = droppedFolder;
                droppedFolder = null;
                if (d) onFolder(d);
              }}
            >
              <FolderUpIcon />Upload a folder
            </Button>
          </div>
        {/if}
        <FileList rows={picked.map((file) => plan?.rows.find((r) => r.file === file) ?? { file })} onRemove={remove} disabled={busy} />
        {#if picked.length}
          {#if repo.format === "maven"}
            <MavenFields files={picked} bind:plan disabled={busy} />
          {:else if repo.format === "debian"}
            <DebianFields {repo} {folder} files={picked} bind:plan disabled={busy} />
          {:else}
            <RawFields {folder} files={picked} bind:plan disabled={busy} />
          {/if}
        {/if}
        {#if error}
          <ErrorState inline {error} />
        {:else if picked.length && plan?.problem}
          <p class="text-sm text-muted-foreground">{plan.problem}</p>
        {/if}
      </div>
      <Dialog.Footer>
        <Button variant="outline" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
        <Button disabled={busy || blocked} onclick={upload}>
          {picked.length ? `Upload ${nFiles(picked.length)}` : "Upload"}
        </Button>
      </Dialog.Footer>
    {:else if phase === "sending"}
      <div class="flex flex-col gap-3" aria-live="polite">
        <div
          class="h-2 overflow-hidden rounded-full bg-muted"
          role="progressbar"
          aria-label="Upload progress"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
        >
          <div class="h-full rounded-full bg-primary transition-[width]" style="width: {percent}%"></div>
        </div>
        {#if storing}
          <p class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner /> Storing the files…</p>
        {:else if progress}
          <p class="text-sm tabular-nums text-muted-foreground">
            Sending {formatSize(progress.sent)} of {formatSize(progress.total)} ({percent}%)
          </p>
        {/if}
      </div>
      <Dialog.Footer>
        <Button variant="outline" disabled={storing} onclick={() => abort?.()}>Cancel upload</Button>
      </Dialog.Footer>
    {:else}
      <div class="flex flex-col gap-4">
        <UploadResults files={results} />
      </div>
      <Dialog.Footer>
        <Button variant="outline" onclick={reset}>Upload more files</Button>
        {#if resultFolder !== null}
          <Button variant="outline" onclick={openFolder}>Open the folder</Button>
        {/if}
        <Button onclick={() => (open = false)}>Done</Button>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>
