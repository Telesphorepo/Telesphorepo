<!--
  Confirms the delete of a file or a folder in Browse (spec §13). For a folder it counts the files
  under it while the dialog is open, and the list and the button say how many go. For a Maven
  version folder it says that maven-metadata.xml keeps listing the version until the next publish;
  for Debian, that apt drops the packages. onConfirm does the delete (the page holds the session).

    <DeleteDialog bind:open={deleteOpen} repo={r} target={deleting} onConfirm={runDelete} />

  target.path is a file path, or a folder path ending in "/" with dir: true.
-->
<script lang="ts">
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import { errorMessage, type Repo } from "$lib/api";
  import { countFolder, type FolderCount } from "./count";

  let {
    open = $bindable(false),
    repo,
    target,
    onConfirm,
  }: {
    open?: boolean;
    repo: Repo;
    target: { path: string; dir: boolean } | null;
    onConfirm: () => Promise<unknown>;
  } = $props();

  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;

  let count = $state<FolderCount | null>(null);
  let counting = $state(false);
  let countError = $state("");
  let seq = 0;

  $effect(() => {
    const t = target;
    const mine = ++seq;
    count = null;
    countError = "";
    counting = false;
    if (!open || !t?.dir) return;
    counting = true;
    countFolder(repo.name, t.path).then(
      (c) => {
        if (mine === seq) count = c;
      },
      (e) => {
        if (mine === seq) countError = errorMessage(e);
      },
    ).finally(() => {
      if (mine === seq) counting = false;
    });
  });

  // <artifactId>/<version>/ of a Maven version folder.
  const version = $derived.by(() => {
    const seg = (target?.path ?? "").replace(/\/$/, "").split("/");
    return seg.length >= 3 ? { artifact: seg[seg.length - 2], version: seg[seg.length - 1] } : null;
  });

  const changes = $derived.by(() => {
    const t = target;
    if (!t) return [];
    const out: string[] = [];
    if (!t.dir) {
      out.push(`Deletes ${t.path} from ${repo.name}`);
      if (repo.format === "debian" && t.path.endsWith(".deb"))
        out.push("apt stops offering the package after the indexes of its distribution rebuild");
    } else {
      if (counting) out.push(`Deletes every file under ${t.path} (counting the files…)`);
      else if (countError) out.push(`Deletes every file under ${t.path} (the files could not be counted: ${countError})`);
      else if (count?.more) out.push(`Deletes more than ${nFiles(count.count)} under ${t.path}`);
      else if (count) out.push(`Deletes ${nFiles(count.count)} under ${t.path}`);
      if (repo.format === "maven" && count?.versionFolder && version)
        out.push(
          `maven-metadata.xml of ${version.artifact} keeps listing version ${version.version} until the next publish of ${version.artifact}`,
        );
      if (repo.format === "debian" && (count?.debs ?? true))
        out.push("apt stops offering the deleted packages after the indexes rebuild");
    }
    out.push("Clients that still need these files get “not found”");
    return out;
  });

  const confirmLabel = $derived.by(() => {
    if (!target?.dir) return "Delete file";
    if (count && !count.more && count.count > 0) return `Delete ${nFiles(count.count)}`;
    return "Delete folder";
  });
</script>

<ConfirmDialog
  bind:open
  title={target?.dir ? "Delete this folder?" : "Delete this file?"}
  description="You cannot undo this."
  {changes}
  {confirmLabel}
  destructive
  {onConfirm}
/>
