<!--
  Upload to a repository (a page: the flow is longer than a short form). First a fresh second
  factor, which creates a publish token for the repo kept in memory (session.ts); then one drop
  zone for files or a folder.
  - Files: the format's fields (Maven coordinates, Debian distribution and component, raw folder),
    then the upload with progress, then what the server stored.
  - A folder: the server checks it first (POST /upload/scan stores nothing), the user unticks what
    should not go and fills the fields, then the ticked files upload. Each file goes with its path
    inside the folder as its multipart filename.
  `folder` is the open folder of the Files tab ("" or ending in "/"); Debian and raw use it for
  their first field values.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, ApiError, errorMessage, type Proof, type Repo, type ScanAnswer, type Uploaded, type UploadProgress } from "$lib/api";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { formatSize } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import FreshFactor from "$lib/components/FreshFactor.svelte";
  import DropZone from "./upload/DropZone.svelte";
  import FileListView from "./upload/FileList.svelte";
  import MavenFields from "./upload/MavenFields.svelte";
  import DebianFields from "./upload/DebianFields.svelte";
  import RawFields from "./upload/RawFields.svelte";
  import ScanPreview from "./upload/ScanPreview.svelte";
  import UploadResults from "./upload/UploadResults.svelte";
  import { filesFromEntries, filesFromInput, folderKeep, type FolderDrop, type FolderFile, type FolderRead } from "./upload/folder";
  import type { UploadPlan } from "./upload/types";
  import { dropSession, keepSession, sessionToken } from "./session";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { repo, folder = "" }: { repo: Repo; folder?: string } = $props();

  // The server takes at most this many files per scan or upload (internal/api/files.go).
  const maxFiles = 500;
  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;

  const texts: Record<string, { hint: string; accept?: string; unit: [string, string] }> = {
    maven: {
      hint: "A pom and its jars (sources, javadoc), a lone jar, or a Maven repository folder.",
      unit: ["artifact", "artifacts"],
    },
    debian: { hint: "One or more .deb packages, or a folder of them.", accept: ".deb", unit: ["package", "packages"] },
    raw: { hint: "Any files, or a folder with its subfolders.", unit: ["file", "files"] },
  };
  const text = $derived(texts[repo.format] ?? texts.raw);
  const unit = (n: number) => text.unit[n === 1 ? 0 : 1];
  const keep = $derived(folderKeep(repo.format));

  // --- the publish token (a fresh second factor) ---

  // sessionToken() is not reactive: bumping tokenVersion makes `confirmed` read it again.
  let tokenVersion = $state(0);
  const confirmed = $derived.by(() => {
    void tokenVersion;
    return !!sessionToken(repo.name);
  });
  let expiredNote = $state("");

  async function createSession(proof: Proof) {
    keepSession(repo.name, await api.publish.manualSession(repo.name, proof));
    expiredNote = "";
    tokenVersion++;
  }

  /** The token for a request, or an error that asks the user to confirm again. */
  function token(): string {
    const t = sessionToken(repo.name);
    if (!t) {
      expiredNote = "Your confirmation has expired. Confirm again, then upload.";
      tokenVersion++;
      throw new Error(expiredNote);
    }
    return t;
  }

  /** Forgets a token the server no longer accepts. */
  function checkSession(e: unknown) {
    if (e instanceof ApiError && e.status === 401) {
      dropSession(repo.name);
      expiredNote = "Your confirmation has expired. Confirm again, then upload.";
      tokenVersion++;
    }
  }

  // --- the flow ---

  type Phase = "pick" | "files" | "reading" | "scanning" | "preview" | "sending" | "done";
  let phase = $state<Phase>("pick");
  let mode = $state<"files" | "folder">("files");
  let error = $state("");
  let progress = $state<UploadProgress | null>(null);
  let results = $state<Uploaded[]>([]);
  let abort: (() => void) | null = null;

  // Files
  let picked = $state<File[]>([]);
  let plan = $state<UploadPlan>();
  // Folder
  let folderFiles = $state<FolderFile[]>([]);
  let skipped = $state(0);
  let answer = $state<ScanAnswer>({ found: [], ignored: [] });
  let selected = $state<boolean[]>([]);

  function reset() {
    phase = "pick";
    error = "";
    progress = null;
    results = [];
    picked = [];
    plan = undefined;
    folderFiles = [];
    skipped = 0;
    answer = { found: [], ignored: [] };
    selected = [];
  }

  const cancelled = (e: unknown) => e instanceof DOMException && e.name === "AbortError";

  function addFiles(list: File[]) {
    error = "";
    mode = "files";
    const byName = new Map(picked.map((f) => [f.name, f]));
    for (const f of list) byName.set(f.name, f);
    let all = [...byName.values()];
    if (all.length > maxFiles) {
      all = all.slice(0, maxFiles);
      error = `Upload at most ${maxFiles} files at a time. The rest were left out.`;
    }
    picked = all;
    phase = "files";
  }

  function removeFile(f: File) {
    picked = picked.filter((x) => x !== f);
    error = "";
    if (picked.length === 0) phase = "pick";
  }

  let moreInput = $state<HTMLInputElement>();

  const emptyText = (read: FolderRead) =>
    read.skipped ? `None of the ${read.skipped.toLocaleString()} files in the folder can go to this repository.` : "The folder is empty.";

  async function readDrop(d: FolderDrop) {
    error = "";
    phase = "reading";
    try {
      await scan(await filesFromEntries(d, maxFiles, keep));
    } catch {
      phase = "pick";
      error = "The browser could not read the folder. Choose it with the button instead.";
    }
  }

  function readInput(list: FileList) {
    error = "";
    scan(filesFromInput(list, maxFiles, keep));
  }

  /** A multipart body: the fields first (the server reads parts in order), then each file. */
  function body(list: { file: File; path: string }[], fields: Record<string, string> = {}): FormData {
    const form = new FormData();
    for (const [k, v] of Object.entries(fields)) if (v) form.append(k, v);
    for (const f of list) form.append("file", f.file, f.path);
    return form;
  }

  async function scan(read: FolderRead) {
    mode = "folder";
    if (!read.files.length) {
      phase = "pick";
      error = emptyText(read);
      return;
    }
    if (read.more) {
      phase = "pick";
      error = `The folder has more than ${maxFiles} files to upload. Choose a smaller folder, such as the one that holds only the packages.`;
      return;
    }
    folderFiles = read.files;
    skipped = read.skipped;
    try {
      const t = token();
      phase = "scanning";
      progress = { sent: 0, total: folderFiles.reduce((n, f) => n + f.file.size, 0) };
      const s = api.repos.scanUpload(repo.name, body(folderFiles), t, (p) => (progress = p));
      abort = s.abort;
      answer = await s.done;
      selected = answer.found.map((f) => f.problems.length === 0);
      phase = "preview";
    } catch (e) {
      phase = "pick";
      if (cancelled(e)) error = "You cancelled the check.";
      else {
        error = errorMessage(e);
        checkSession(e);
      }
    } finally {
      abort = null;
      progress = null;
    }
  }

  // The folder files of the ticked items, in folder order.
  const chosen = $derived.by(() => {
    const paths = new Set(answer.found.flatMap((f, i) => (selected[i] && f.problems.length === 0 ? f.files : [])));
    return folderFiles.filter((f) => paths.has(f.path));
  });
  const nItems = $derived(answer.found.filter((f, i) => selected[i] && f.problems.length === 0).length);
  // Maven takes no fields for a folder; Debian and raw report theirs through plan.
  const folderNeedsFields = $derived(repo.format === "debian" || repo.format === "raw");

  const problem = $derived.by(() => {
    if (mode === "files") return !picked.length ? "Add files to upload." : (plan?.problem ?? "");
    if (chosen.length === 0) return `Tick at least one ${text.unit[0]} to upload.`;
    return folderNeedsFields ? (plan?.problem ?? "") : "";
  });

  // Raw folder: where the first file lands, so the user sees that paths inside the folder stay.
  const rawExample = $derived.by(() => {
    if (repo.format !== "raw" || mode !== "folder" || !chosen.length) return "";
    const target = plan?.fields.folder ?? "";
    return target ? `${target}/${chosen[0].path}` : chosen[0].path;
  });

  async function upload() {
    if (problem) return;
    error = "";
    const back: Phase = mode === "files" ? "files" : "preview";
    const list = mode === "files" ? picked.map((file) => ({ file, path: file.name })) : chosen;
    const fields = mode === "files" || folderNeedsFields ? (plan?.fields ?? {}) : {};
    try {
      const t = token();
      phase = "sending";
      progress = { sent: 0, total: list.reduce((n, f) => n + f.file.size, 0) };
      const s = api.repos.upload(repo.name, body(list, fields), t, (p) => (progress = p));
      abort = s.abort;
      const a = await s.done;
      results = a.files;
      phase = "done";
      toast.success(
        mode === "files"
          ? `Uploaded ${nFiles(picked.length)} to ${repo.name}`
          : `Uploaded ${nItems.toLocaleString()} ${unit(nItems)} to ${repo.name}`,
      );
    } catch (e) {
      phase = back;
      if (cancelled(e)) error = "You cancelled the upload. Nothing was stored.";
      else {
        error = errorMessage(e);
        checkSession(e);
      }
    } finally {
      abort = null;
      progress = null;
    }
  }

  // The folder that holds the first stored file, to open after the upload.
  const resultFolder = $derived.by(() => {
    const first = results.find((f) => !/\.(md5|sha1|sha256)$/.test(f.path));
    if (!first) return "";
    const i = first.path.lastIndexOf("/");
    return i < 0 ? "" : first.path.slice(0, i + 1);
  });

  const percent = $derived(progress && progress.total ? Math.min(100, Math.round((progress.sent / progress.total) * 100)) : 0);
  const storing = $derived(!!progress && progress.total > 0 && progress.sent >= progress.total);
  const uploadLabel = $derived(
    mode === "files"
      ? picked.length ? `Upload ${nFiles(picked.length)}` : "Upload"
      : nItems ? `Upload ${nItems.toLocaleString()} ${unit(nItems)}` : "Upload",
  );
</script>

{#snippet progressBar(label: string, sendingText: string, waitingText: string)}
  <div class="flex flex-col gap-3" aria-live="polite">
    <div class="h-2 overflow-hidden rounded-full bg-muted" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent}>
      <div class="h-full rounded-full bg-primary transition-[width]" style="width: {percent}%"></div>
    </div>
    {#if storing}
      <p class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner /> {waitingText}</p>
    {:else if progress}
      <p class="text-sm text-muted-foreground tabular-nums">{sendingText}: {formatSize(progress.sent)} of {formatSize(progress.total)} ({percent}%)</p>
    {/if}
  </div>
{/snippet}

{#snippet fields()}
  {#if repo.format === "maven" && mode === "files"}
    <MavenFields files={picked} bind:plan />
  {:else if repo.format === "debian"}
    <DebianFields {repo} {folder} files={mode === "files" ? picked : chosen.map((f) => f.file)} bind:plan />
  {:else if repo.format === "raw"}
    <RawFields {folder} files={mode === "files" ? picked : chosen.map((f) => f.file)} bind:plan />
    {#if rawExample}
      <p class="text-sm text-muted-foreground">Files keep their paths inside the folder, e.g. <span class="break-all">{rawExample}</span>.</p>
    {/if}
  {/if}
{/snippet}

<PageHeader
  title="Upload to {repo.name}"
  crumbs={[
    { label: "Repositories", href: href("/") },
    { label: repo.name, href: href(filesPath(repo.name, folder)) },
    { label: "Upload" },
  ]}
/>

<FormPage>
  {#if !confirmed}
    <FormSection
      title="Confirm it is you"
      description="Uploads need a fresh check with your authenticator app or a passkey. It lasts a short time, so you can upload several times in a row."
    >
      {#if expiredNote}<div class="mb-4"><ErrorState inline error={expiredNote} /></div>{/if}
      <FreshFactor actionLabel="Continue" onProof={createSession} />
    </FormSection>
  {:else if phase === "pick" || phase === "reading"}
    <FormSection>
      {#if phase === "reading"}
        <p class="flex items-center gap-2 py-12 text-sm text-muted-foreground"><Spinner /> Reading the folder…</p>
      {:else}
        <DropZone onFiles={addFiles} onFolderDrop={readDrop} onFolderInput={readInput} hint={text.hint} accept={text.accept} />
      {/if}
      {#if error}<div class="mt-4"><ErrorState inline {error} /></div>{/if}
    </FormSection>
  {:else if phase === "files"}
    <FormSection title="Files">
      {#snippet actions()}
        <Button variant="outline" size="sm" onclick={() => moreInput?.click()}><PlusIcon />Add files</Button>
      {/snippet}
      <FileListView rows={picked.map((file) => plan?.rows.find((r) => r.file === file) ?? { file })} onRemove={removeFile} />
      <input
        bind:this={moreInput}
        type="file"
        multiple
        accept={text.accept}
        class="sr-only"
        tabindex="-1"
        aria-hidden="true"
        onchange={(e) => {
          const list = [...(e.currentTarget.files ?? [])];
          e.currentTarget.value = "";
          if (list.length) addFiles(list);
        }}
      />
    </FormSection>
    <FormSection title={repo.format === "maven" ? "Coordinates" : "Where they go"}>
      {@render fields()}
      {#snippet footer()}
        <Button disabled={!!problem} onclick={upload}>{uploadLabel}</Button>
        <Button variant="ghost" onclick={reset}>Start over</Button>
        {#if error}
          <ErrorState inline {error} />
        {:else if problem}
          <p class="text-sm text-muted-foreground">{problem}</p>
        {/if}
      {/snippet}
    </FormSection>
  {:else if phase === "scanning"}
    <FormSection title="Checking the folder" description="Nothing is stored yet.">
      {@render progressBar("Check progress", `Sending ${nFiles(folderFiles.length)} for a check`, "Checking the files…")}
      {#snippet footer()}
        <Button variant="outline" disabled={storing} onclick={() => abort?.()}>Cancel the check</Button>
      {/snippet}
    </FormSection>
  {:else if phase === "preview"}
    <FormSection title="What the folder holds" description="Nothing is stored until you upload.">
      <div class="flex flex-col gap-4">
        <ScanPreview {answer} {skipped} bind:selected format={repo.format} />
      </div>
    </FormSection>
    {#if folderNeedsFields && chosen.length}
      <FormSection title="Where they go">{@render fields()}</FormSection>
    {/if}
    <FormSection>
      <div class="flex flex-wrap items-center gap-2">
        <Button disabled={!!problem} onclick={upload}>{uploadLabel}</Button>
        <Button variant="ghost" onclick={reset}>Choose another folder</Button>
        {#if error}
          <ErrorState inline {error} />
        {:else if answer.found.length && problem}
          <p class="text-sm text-muted-foreground">{problem}</p>
        {/if}
      </div>
    </FormSection>
  {:else if phase === "sending"}
    <FormSection title="Uploading">
      {@render progressBar("Upload progress", "Sending", "Storing the files…")}
      {#snippet footer()}
        <Button variant="outline" disabled={storing} onclick={() => abort?.()}>Cancel the upload</Button>
      {/snippet}
    </FormSection>
  {:else}
    <FormSection title="Uploaded">
      <UploadResults files={results} />
      {#snippet footer()}
        <Button onclick={() => navigate(filesPath(repo.name, resultFolder))}>Open the folder</Button>
        <Button variant="outline" onclick={reset}>Upload more</Button>
      {/snippet}
    </FormSection>
  {/if}
</FormPage>
