<!--
  Upload a whole folder to a repo from Browse (spec §13.1): drop or choose a folder, the server
  checks it (POST /api/repos/{name}/upload/scan, which stores nothing), the user unticks what
  should not go and fills the format's fields, then the confirmed files upload with progress.
  Each file goes with its path inside the folder as its multipart filename. The page holds the
  publish session, as for UploadDialog.

    <FolderUploadDialog bind:open {repo} {folder} {token} {dropped} onError={(e, t) => checkSession(e, t)}
      onUploaded={() => version++} />

  For Maven and Debian only the files that can be part of an upload go to the check (folderKeep);
  the preview counts the rest as left out. dropped is a folder that the user dropped on "Upload
  files"; the dialog reads it when it opens.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import * as Dialog from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import { api, errorMessage, type Repo, type ScanAnswer, type Uploaded, type UploadProgress } from "$lib/api";
  import { encodePath, navigate } from "$lib/router.svelte";
  import { formatSize } from "$lib/format";
  import FolderDropZone from "./FolderDropZone.svelte";
  import ScanPreview from "./ScanPreview.svelte";
  import DebianFields from "./DebianFields.svelte";
  import RawFields from "./RawFields.svelte";
  import UploadResults from "./UploadResults.svelte";
  import { folderKeep, type FolderDrop, type FolderFile, type FolderRead } from "./folder";
  import type { UploadPlan } from "./types";

  let {
    open = $bindable(false),
    repo,
    folder = "",
    token,
    dropped = null,
    onError,
    onUploaded,
  }: {
    open?: boolean;
    repo: Repo;
    /** The open folder ("" or ending in "/"); Debian and raw use it for their first field values. */
    folder?: string;
    /** The publish session token for repo; throws when the session has ended. */
    token: () => string;
    /** A folder dropped on "Upload files", read when the dialog opens. */
    dropped?: FolderDrop | null;
    /** Hears a failed request and the token it used. */
    onError?: (e: unknown, token: string | undefined) => void;
    /** Runs after a successful upload, e.g. to reload the tree. */
    onUploaded?: (files: Uploaded[]) => void;
  } = $props();

  // The server takes at most this many files per scan or upload (internal/api/files.go).
  const maxFiles = 500;

  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;

  const texts: Record<string, { description: string; hint: string; unit: [string, string] }> = {
    maven: {
      description:
        "Drop a Maven repository folder (com/example/app/1.0/...) or a build output folder with poms and jars. The server checks it first. Nothing is stored until you confirm.",
      hint: "A Maven repository folder, or a folder with poms and their jars",
      unit: ["artifact", "artifacts"],
    },
    debian: {
      description: "Drop a folder of .deb packages. The server checks each package first. Nothing is stored until you confirm.",
      hint: "A folder with .deb packages",
      unit: ["package", "packages"],
    },
    raw: {
      description: "Drop a folder to upload it with its subfolders. You see the files first. Nothing is stored until you confirm.",
      hint: "Any folder",
      unit: ["file", "files"],
    },
  };
  const text = $derived(texts[repo.format] ?? texts.raw);

  let phase = $state<"pick" | "scanning" | "preview" | "sending" | "done">("pick");
  let files = $state<FolderFile[]>([]);
  let skipped = $state(0);
  let initial = $state<FolderDrop | null>(null);
  const keep = $derived(folderKeep(repo.format));
  let answer = $state<ScanAnswer>({ found: [], ignored: [] });
  let selected = $state<boolean[]>([]);
  let plan = $state<UploadPlan>();
  let error = $state("");
  let progress = $state<UploadProgress | null>(null);
  let results = $state<Uploaded[]>([]);
  let abort: (() => void) | null = null;

  function reset() {
    phase = "pick";
    files = [];
    skipped = 0;
    initial = null;
    answer = { found: [], ignored: [] };
    selected = [];
    plan = undefined;
    error = "";
    progress = null;
    results = [];
  }

  $effect(() => {
    if (open)
      untrack(() => {
        reset();
        initial = dropped;
      });
  });

  const busy = $derived(phase === "scanning" || phase === "sending");

  /** A multipart body: the fields first (the server reads parts in order), then each file with its path. */
  function body(list: FolderFile[], fields: Record<string, string> = {}): FormData {
    const form = new FormData();
    for (const [k, v] of Object.entries(fields)) if (v) form.append(k, v);
    for (const f of list) form.append("file", f.file, f.path);
    return form;
  }

  const cancelled = (e: unknown) => e instanceof DOMException && e.name === "AbortError";

  async function scan(read: FolderRead) {
    error = "";
    initial = null; // read once: a later "pick" phase shows an empty drop zone
    if (read.more) {
      error = `The folder has more than ${maxFiles} files to upload. Choose a smaller folder, such as the one that holds only the packages.`;
      return;
    }
    files = read.files;
    skipped = read.skipped;
    let t: string | undefined;
    try {
      t = token();
      phase = "scanning";
      progress = { sent: 0, total: files.reduce((n, f) => n + f.file.size, 0) };
      const s = api.repos.scanUpload(repo.name, body(files), t, (p) => (progress = p));
      abort = s.abort;
      answer = await s.done;
      selected = answer.found.map((f) => f.problems.length === 0);
      phase = "preview";
    } catch (e) {
      phase = "pick";
      if (cancelled(e)) error = "You cancelled the check.";
      else {
        error = errorMessage(e);
        onError?.(e, t);
      }
    } finally {
      abort = null;
      progress = null;
    }
  }

  // The files of the ticked items, in folder order.
  const chosen = $derived.by(() => {
    const paths = new Set(answer.found.flatMap((f, i) => (selected[i] && f.problems.length === 0 ? f.files : [])));
    return files.filter((f) => paths.has(f.path));
  });
  const chosenFiles = $derived(chosen.map((f) => f.file));
  const nItems = $derived(answer.found.filter((f, i) => selected[i] && f.problems.length === 0).length);
  const unit = (n: number) => text.unit[n === 1 ? 0 : 1];

  // Maven takes no fields for a folder; Debian and raw report theirs through plan.
  const needsFields = $derived(repo.format === "debian" || repo.format === "raw");
  const problem = $derived(
    chosen.length === 0 ? `Tick at least one ${text.unit[0]} to upload.` : needsFields ? (plan?.problem ?? "") : "",
  );

  // Raw: where the first file lands, so the user sees that paths inside the folder stay.
  const rawExample = $derived.by(() => {
    if (repo.format !== "raw" || !chosen.length) return "";
    const target = plan?.fields.folder ?? "";
    return target ? `${target}/${chosen[0].path}` : chosen[0].path;
  });

  async function upload() {
    if (problem) return;
    error = "";
    let t: string | undefined;
    try {
      t = token();
      phase = "sending";
      progress = { sent: 0, total: chosen.reduce((n, f) => n + f.file.size, 0) };
      const s = api.repos.upload(repo.name, body(chosen, needsFields ? plan?.fields : {}), t, (p) => (progress = p));
      abort = s.abort;
      const a = await s.done;
      results = a.files;
      phase = "done";
      toast.success(`Uploaded ${nItems.toLocaleString()} ${unit(nItems)} to ${repo.name}`);
      onUploaded?.(a.files);
    } catch (e) {
      phase = "preview";
      if (cancelled(e)) error = "You cancelled the upload. Nothing was stored.";
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

{#snippet progressBar(label: string, sendingText: string, waitingText: string)}
  <div class="flex flex-col gap-3" aria-live="polite">
    <div
      class="h-2 overflow-hidden rounded-full bg-muted"
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
    >
      <div class="h-full rounded-full bg-primary transition-[width]" style="width: {percent}%"></div>
    </div>
    {#if storing}
      <p class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner /> {waitingText}</p>
    {:else if progress}
      <p class="text-sm tabular-nums text-muted-foreground">
        {sendingText}: {formatSize(progress.sent)} of {formatSize(progress.total)} ({percent}%)
      </p>
    {/if}
  </div>
{/snippet}

<Dialog.Root bind:open={() => open, (v) => { if (!busy) open = v; }}>
  <Dialog.Content class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
    <Dialog.Header>
      <Dialog.Title>Upload a folder to {repo.name}</Dialog.Title>
      <Dialog.Description>{text.description}</Dialog.Description>
    </Dialog.Header>

    {#if phase === "pick"}
      <div class="flex flex-col gap-4">
        <FolderDropZone onRead={scan} limit={maxFiles} hint={text.hint} {keep} {initial} />
        {#if error}<ErrorState inline {error} />{/if}
      </div>
      <Dialog.Footer>
        <Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
      </Dialog.Footer>
    {:else if phase === "scanning"}
      {@render progressBar("Check progress", `Sending ${nFiles(files.length)} for a check`, "Checking the files…")}
      <p class="text-sm text-muted-foreground">Nothing is stored yet.</p>
      <Dialog.Footer>
        <Button variant="outline" disabled={storing} onclick={() => abort?.()}>Cancel the check</Button>
      </Dialog.Footer>
    {:else if phase === "preview"}
      <div class="flex flex-col gap-4">
        <ScanPreview {answer} {skipped} bind:selected format={repo.format} />
        {#if chosen.length}
          {#if repo.format === "debian"}
            <DebianFields {repo} {folder} files={chosenFiles} bind:plan />
          {:else if repo.format === "raw"}
            <RawFields {folder} files={chosenFiles} bind:plan />
            {#if rawExample}
              <p class="text-sm text-muted-foreground">
                Files keep their paths inside the folder, e.g. <span class="font-mono break-all">{rawExample}</span>.
              </p>
            {/if}
          {/if}
        {/if}
        {#if error}
          <ErrorState inline {error} />
        {:else if answer.found.length && problem}
          <p class="text-sm text-muted-foreground">{problem}</p>
        {/if}
      </div>
      <Dialog.Footer>
        <Button variant="outline" onclick={reset}>Choose another folder</Button>
        <Button disabled={!!problem} onclick={upload}>
          {nItems ? `Upload ${nItems.toLocaleString()} ${unit(nItems)}` : "Upload"}
        </Button>
      </Dialog.Footer>
    {:else if phase === "sending"}
      {@render progressBar("Upload progress", "Sending", "Storing the files…")}
      <Dialog.Footer>
        <Button variant="outline" disabled={storing} onclick={() => abort?.()}>Cancel upload</Button>
      </Dialog.Footer>
    {:else}
      <div class="flex flex-col gap-4">
        <UploadResults files={results} />
      </div>
      <Dialog.Footer>
        <Button variant="outline" onclick={reset}>Upload another folder</Button>
        {#if resultFolder !== null}
          <Button variant="outline" onclick={openFolder}>Open the folder</Button>
        {/if}
        <Button onclick={() => (open = false)}>Done</Button>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>
