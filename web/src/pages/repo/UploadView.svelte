<!--
  Upload to a repository (a page: the flow is longer than a short form). First a fresh second
  factor, which creates a publish token for the repo kept in memory (session.ts); then one drop
  zone for files or a folder, as the format's descriptor (upload) allows. The server plans every
  upload (POST /upload/plan stores nothing): the browser sends the list of files and the first
  bytes of the files the format reads, and shows what the plan stores, where each file goes, the
  problems, the files it leaves out and the fields of the format (values it read from the files
  show fixed). An edit of a field plans again, and aborts a check in flight; the server holds the
  first bytes it read (heads), so a later plan sends only the fields, the list and the first bytes
  of new files (a 409 sends them all again). The ticked items then upload with progress: past
  90 MB, the largest files first go through resumable uploads in 16 MiB chunks (upload/send.ts),
  and the upload names them in its part "uploads"; each other file goes with its path in the
  upload as its multipart filename. The server plans them once more before it stores them. `folder` is the open folder of the Files tab ("" or ending in "/");
  the plan takes the first values of fields from it.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import {
    api,
    ApiError,
    errorMessage,
    type FinishedUpload,
    type Proof,
    type Repo,
    type UploadPlan,
    type Uploaded,
    type UploadProgress,
  } from "$lib/api";
  import { formats } from "$lib/formats/registry.svelte";
  import { filesPath, href, navigate } from "$lib/router.svelte";
  import { formatSize } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import FreshFactor from "$lib/components/FreshFactor.svelte";
  import DropZone from "./upload/DropZone.svelte";
  import PlanView from "./upload/PlanView.svelte";
  import UploadResults from "./upload/UploadResults.svelte";
  import { filesFromEntries, filesFromInput, keepOf, type FolderDrop, type FolderFile, type FolderRead } from "./upload/folder";
  import { resumableOf, sendResumable } from "./upload/send";
  import { dropSession, keepSession, sessionToken } from "./session";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import UploadIcon from "@lucide/svelte/icons/upload";

  let { repo, folder = "" }: { repo: Repo; folder?: string } = $props();

  // The server takes at most this many files per plan or upload (internal/api/files.go).
  const maxFiles = 500;
  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;

  const up = $derived(formats.get(repo.format)?.upload ?? null);
  const word = (n: number) => (n === 1 ? (up?.one ?? "file") : (up?.many ?? "files"));
  const keep = $derived(keepOf(up?.keep ?? []));

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

  type Phase = "pick" | "reading" | "planning" | "plan" | "sending" | "done";
  let phase = $state<Phase>("pick");
  // The files of the upload with their paths: a chosen file by its name, a folder's by its path in it.
  let files = $state<FolderFile[]>([]);
  // A dropped folder: no files can be added to it.
  let fromFolder = $state(false);
  let skipped = $state(0);
  let plan = $state<UploadPlan | null>(null);
  let selected = $state<boolean[]>([]);
  // The values the person typed, by field key; the plan gives the others.
  let typed = $state<Record<string, string>>({});
  let error = $state("");
  let planError = $state("");
  let progress = $state<UploadProgress | null>(null);
  let results = $state<Uploaded[]>([]);
  let abort: (() => void) | null = null;

  function reset() {
    phase = "pick";
    files = [];
    fromFolder = false;
    skipped = 0;
    plan = null;
    selected = [];
    typed = {};
    error = "";
    planError = "";
    progress = null;
    results = [];
    planSeq++;
    planAbort?.();
    forgetHeads();
  }

  const cancelled = (e: unknown) => e instanceof DOMException && e.name === "AbortError";

  function addFiles(list: File[]) {
    error = "";
    const byName = new Map(files.map((f) => [f.path, f]));
    for (const file of list) {
      byName.set(file.name, { file, path: file.name });
      held.delete(file.name);
    }
    let all = [...byName.values()];
    if (all.length > maxFiles) {
      all = all.slice(0, maxFiles);
      error = `Upload at most ${maxFiles} files at a time. The rest were left out.`;
    }
    files = all;
    fromFolder = false;
    runPlan(true);
  }

  let moreInput = $state<HTMLInputElement>();

  const emptyText = (read: FolderRead) =>
    read.skipped ? `None of the ${read.skipped.toLocaleString()} files in the folder can go to this repository.` : "The folder is empty.";

  async function readDrop(d: FolderDrop) {
    error = "";
    phase = "reading";
    try {
      useFolder(await filesFromEntries(d, maxFiles, keep));
    } catch {
      phase = "pick";
      error = "The browser could not read the folder. Choose it with the button instead.";
    }
  }

  function readInput(list: FileList) {
    error = "";
    useFolder(filesFromInput(list, maxFiles, keep));
  }

  function useFolder(read: FolderRead) {
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
    forgetHeads();
    files = read.files;
    skipped = read.skipped;
    fromFolder = true;
    runPlan(true);
  }

  // --- the plan ---

  let planSeq = 0;
  let planTimer: ReturnType<typeof setTimeout> | undefined;
  // The plan request in flight: a new plan aborts it.
  let planAbort: (() => void) | null = null;
  // The first bytes the server holds (heads of the last plan) and the paths it holds them for.
  let heads = "";
  let held = new Set<string>();

  function forgetHeads() {
    heads = "";
    held = new Set();
  }

  /** Plans the files now (first: the plan of new files, with a progress bar), or 400 ms after the last edit of a field. */
  function runPlan(first: boolean) {
    clearTimeout(planTimer);
    if (!up || !files.length) return;
    if (first) {
      void plannow(true);
      return;
    }
    planTimer = setTimeout(() => void plannow(false), 400);
  }

  async function plannow(first: boolean) {
    if (!up) return;
    const mine = ++planSeq;
    planError = "";
    let t: string;
    try {
      t = token();
    } catch (e) {
      phase = "pick";
      error = errorMessage(e);
      return;
    }
    if (first) {
      phase = "planning";
      progress = { sent: 0, total: 0 };
    }
    planAbort?.();
    const reads = up.reads;
    const list = files;
    const send = (withHeads: boolean) => {
      const req = { fields: { ...typed }, folder, files: list, heads: withHeads ? heads : "", held };
      const r = api.repos.uploadPlan(repo.name, req, reads, t, (p) => {
        if (first && mine === planSeq) progress = p;
      });
      planAbort = r.abort;
      if (first) abort = r.abort;
      return r.done;
    };
    try {
      let p: UploadPlan;
      try {
        p = await send(!!heads);
      } catch (e) {
        // The server no longer holds the first bytes: send them again.
        if (!(e instanceof ApiError && e.status === 409 && heads) || mine !== planSeq) throw e;
        forgetHeads();
        p = await send(false);
      }
      if (mine !== planSeq) return;
      if (p.heads) {
        heads = p.heads;
        held = new Set(list.map((f) => f.path));
      } else forgetHeads();
      const before = plan;
      plan = p;
      // Keep the choice of the items that stay; a new item is ticked when it can go.
      selected = p.items.map((it, i) => {
        if (it.problems.length) return false;
        const old = before?.items.findIndex((b) => b.title === it.title) ?? -1;
        return old >= 0 && !first ? selected[old] !== false : true;
      });
      phase = "plan";
    } catch (e) {
      if (mine !== planSeq) return;
      checkSession(e);
      if (first) {
        phase = files.length && plan ? "plan" : "pick";
        error = cancelled(e) ? "You cancelled the check." : errorMessage(e);
      } else planError = errorMessage(e);
    } finally {
      if (mine === planSeq) planAbort = null;
      if (first && mine === planSeq) {
        abort = null;
        progress = null;
      }
    }
  }

  function setField(key: string, v: string) {
    typed = { ...typed, [key]: v };
    runPlan(false);
  }

  const valueOf = (key: string) => typed[key] ?? plan?.fields.find((f) => f.key === key)?.value ?? "";

  // The files of the ticked items, in upload order.
  const chosen = $derived.by(() => {
    if (!plan) return [];
    const paths = new Set(plan.items.flatMap((it, i) => (selected[i] && it.problems.length === 0 ? it.files.map((f) => f.path) : [])));
    return files.filter((f) => paths.has(f.path));
  });
  const nItems = $derived(plan ? plan.items.filter((it, i) => selected[i] && it.problems.length === 0).length : 0);

  const problem = $derived.by(() => {
    if (!plan) return "";
    const field = plan.fields.find((f) => f.problem);
    if (field) return `${field.label}: ${field.problem}`;
    if (plan.items.length === 1 && plan.items[0].problems.length) return plan.items[0].problems[0];
    if (!plan.items.length) return up?.empty || "The upload has no file that this repository takes.";
    if (nItems === 0) return `Tick at least one ${word(1)} to upload.`;
    return "";
  });

  async function upload() {
    if (problem || !plan) return;
    error = "";
    const ctl = new AbortController();
    const sent: FinishedUpload[] = [];
    let t = "";
    try {
      t = token();
      const total = chosen.reduce((n, f) => n + f.file.size, 0);
      phase = "sending";
      progress = { sent: 0, total };
      abort = () => ctl.abort();
      // The large files first, each through a resumable upload in chunks.
      const big = resumableOf(chosen);
      let base = 0;
      for (const f of chosen) {
        if (!big.has(f)) continue;
        sent.push(await sendResumable(repo.name, f, t, ctl.signal, (n) => (progress = { sent: base + n, total })));
        base += f.file.size;
      }
      const form = new FormData();
      // The fields first (the server reads parts in order), then the finished uploads, then each file.
      for (const f of plan.fields) if (!f.read) form.append(f.key, valueOf(f.key));
      if (sent.length) form.append("uploads", JSON.stringify(sent));
      for (const f of chosen) if (!big.has(f)) form.append("file", f.file, f.path);
      if (ctl.signal.aborted) throw new DOMException("The upload was cancelled.", "AbortError");
      const s = api.repos.upload(repo.name, form, t, (p) => (progress = { sent: base + p.sent, total: base + p.total }));
      ctl.signal.addEventListener("abort", s.abort);
      const a = await s.done;
      results = a.files;
      phase = "done";
      toast.success(`Uploaded ${nItems.toLocaleString()} ${word(nItems)} to ${repo.name}`);
    } catch (e) {
      // The uploads that the server did not finish go now.
      for (const u of sent) void api.repos.cancelUpload(repo.name, u.id, t).catch(() => {});
      phase = "plan";
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
    const first = results.find((f) => !f.generated);
    if (!first) return "";
    const i = first.path.lastIndexOf("/");
    return i < 0 ? "" : first.path.slice(0, i + 1);
  });

  const percent = $derived(progress && progress.total ? Math.min(100, Math.round((progress.sent / progress.total) * 100)) : 0);
  const storing = $derived(!!progress && progress.total > 0 && progress.sent >= progress.total);
  const uploadLabel = $derived(nItems ? `Upload ${nItems.toLocaleString()} ${word(nItems)}` : "Upload");
</script>

{#snippet progressBar(label: string, sendingText: string, waitingText: string)}
  <div class="flex flex-col gap-3" aria-live="polite">
    <div class="h-2 overflow-hidden rounded-full bg-muted" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent}>
      <div class="h-full rounded-full bg-primary transition-[width]" style="width: {percent}%"></div>
    </div>
    {#if storing || !progress?.total}
      <p class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner /> {waitingText}</p>
    {:else}
      <p class="text-sm text-muted-foreground tabular-nums">{sendingText}: {formatSize(progress.sent)} of {formatSize(progress.total)} ({percent}%)</p>
    {/if}
  </div>
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
  {#if !up}
    <EmptyState icon={UploadIcon} title="This format takes no uploads" description="Publish to {repo.name} with the tools on its publish page." />
  {:else if !confirmed}
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
        <DropZone
          onFiles={addFiles}
          onFolderDrop={readDrop}
          onFolderInput={readInput}
          hint={up.hint}
          accept={up.accept.join(",") || undefined}
          files={up.single}
          folders={up.folder}
        />
      {/if}
      {#if error}<div class="mt-4"><ErrorState inline {error} /></div>{/if}
    </FormSection>
  {:else if phase === "planning"}
    <FormSection title="Checking the files" description="Nothing is stored yet.">
      {@render progressBar("Check progress", `Sending ${nFiles(files.length)} for a check`, "Checking the files…")}
      {#snippet footer()}
        <Button variant="outline" disabled={storing} onclick={() => abort?.()}>Cancel the check</Button>
      {/snippet}
    </FormSection>
  {:else if phase === "plan" && plan}
    <FormSection title="What the upload holds" description="Nothing is stored until you upload.">
      {#snippet actions()}
        {#if !fromFolder && up.single}
          <Button variant="outline" size="sm" onclick={() => moreInput?.click()}><PlusIcon />Add files</Button>
        {/if}
      {/snippet}
      <div class="flex flex-col gap-4">
        <PlanView {plan} upload={up} {skipped} bind:selected />
        {#if up.note && plan.items.length}<p class="text-sm text-muted-foreground">{up.note}</p>{/if}
      </div>
      <input
        bind:this={moreInput}
        type="file"
        multiple
        accept={up.accept.join(",") || undefined}
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
    {#if plan.fields.length && plan.items.length}
      <FormSection title="Where they go">
        <FormGrid>
          {#each plan.fields as f (f.key)}
            {@const fid = `upload-${f.key}`}
            <FormRow label={f.label} for={fid} help={f.help} error={f.problem}>
              {#if f.read}
                <div id={fid} class="flex min-h-9 items-center gap-2">
                  <span class="text-body break-all">{f.value}</span>
                  <Badge variant="secondary">from the files</Badge>
                </div>
              {:else if f.type === "enum"}
                <Select.Root type="single" value={valueOf(f.key)} onValueChange={(v) => setField(f.key, v)}>
                  <Select.Trigger id={fid} class="w-full"><span class="truncate">{valueOf(f.key) || "Choose"}</span></Select.Trigger>
                  <Select.Content>
                    {#each f.options ?? [] as o (o)}<Select.Item value={o} label={o}>{o}</Select.Item>{/each}
                  </Select.Content>
                </Select.Root>
              {:else}
                <Input
                  id={fid}
                  placeholder={f.placeholder}
                  autocomplete="off"
                  spellcheck={false}
                  aria-describedby={f.help ? `${fid}-help` : undefined}
                  aria-invalid={f.problem ? true : undefined}
                  value={valueOf(f.key)}
                  oninput={(e) => setField(f.key, e.currentTarget.value)}
                />
              {/if}
            </FormRow>
          {/each}
        </FormGrid>
      </FormSection>
    {/if}
    <FormSection>
      <div class="flex flex-wrap items-center gap-2">
        <Button disabled={!!problem || !!planError} onclick={upload}>{uploadLabel}</Button>
        <Button variant="ghost" onclick={reset}>Start over</Button>
        {#if error}
          <ErrorState inline {error} />
        {:else if planError}
          <ErrorState inline error={planError} />
        {:else if problem}
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
