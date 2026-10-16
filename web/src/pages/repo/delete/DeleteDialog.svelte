<!--
  Confirms the delete of a file, a folder or a version in Browse. While the dialog is open the
  server plans the delete (POST /api/repos/{name}/files/delete/plan, which deletes nothing): the
  number of files that go, and the delete notes of the format with the files each one counts, such
  as the versions that maven-metadata.xml stops listing or the packages that apt stops offering. A
  version without a folder of its own (a Debian package) goes as the list of its files. onConfirm
  does the delete (the page holds the session).

    <DeleteDialog bind:open={deleteOpen} repo={r} target={deleting} onConfirm={runDelete} />

  target.path is a file path, or a folder path ending in "/" with dir: true; with files, the
  artifact folder of version target.version.
-->
<script lang="ts">
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import { api, errorMessage, type DeletePlan, type FilesDelete, type Repo } from "$lib/api";
  import type { DeleteTarget } from "../files/FolderList.svelte";

  let {
    open = $bindable(false),
    repo,
    target,
    onConfirm,
  }: {
    open?: boolean;
    repo: Repo;
    target: DeleteTarget | null;
    onConfirm: () => Promise<unknown>;
  } = $props();

  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;

  let plan = $state<DeletePlan | null>(null);
  let planning = $state(false);
  let planError = $state("");
  let seq = 0;

  const request = (t: DeleteTarget): FilesDelete => (t.files ? { paths: t.files } : t.dir ? { prefixes: [t.path] } : { paths: [t.path] });

  $effect(() => {
    const t = target;
    const mine = ++seq;
    plan = null;
    planError = "";
    planning = false;
    if (!open || !t) return;
    planning = true;
    api.repos
      .deletePlan(repo.name, request(t))
      .then(
        (p) => {
          if (mine === seq) plan = p;
        },
        (e) => {
          if (mine === seq) planError = errorMessage(e);
        },
      )
      .finally(() => {
        if (mine === seq) planning = false;
      });
  });

  const changes = $derived.by(() => {
    const t = target;
    if (!t) return [];
    const out: string[] = [];
    if (t.files) out.push(`Deletes ${nFiles(t.files.length)} of version ${t.version} from ${t.path}`);
    else if (!t.dir) out.push(`Deletes ${t.path} from ${repo.name}`);
    else if (planning) out.push(`Deletes every file under ${t.path} (counting the files…)`);
    else if (planError) out.push(`Deletes every file under ${t.path} (the files could not be counted: ${planError})`);
    else if (plan?.more) out.push(`Deletes more than ${nFiles(plan.files)} under ${t.path}`);
    else if (plan) out.push(`Deletes ${nFiles(plan.files)} under ${t.path}`);
    for (const n of plan?.notes ?? []) out.push(`${n.count.toLocaleString()} ${n.count === 1 ? n.one : n.many}: ${n.text}`);
    out.push("Clients that still need these files get “not found”");
    return out;
  });

  const confirmLabel = $derived.by(() => {
    if (target?.files) return `Delete ${nFiles(target.files.length)}`;
    if (!target?.dir) return "Delete file";
    if (plan && !plan.more && plan.files > 0) return `Delete ${nFiles(plan.files)}`;
    return "Delete folder";
  });
</script>

<ConfirmDialog
  bind:open
  title={target?.files ? `Delete version ${target.version}?` : target?.dir ? "Delete this folder?" : "Delete this file?"}
  description="You cannot undo this."
  {changes}
  {confirmLabel}
  destructive
  {onConfirm}
/>
