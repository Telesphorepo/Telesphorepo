<!--
  Confirms a delete in a group: one file, folder or version, or several picked rows. A group holds
  no files, so the delete goes to its repositories. While the dialog is open the server says which
  repositories hold each item and where the person may delete (POST /api/repos/{group}/files/holders),
  and for one folder each of those repositories plans its delete (the count of files and the
  delete notes of the format). When one repository holds the items, the confirm names it. When
  several do, the person chooses: delete from all of them, or only from the one that serves each
  item (the group then serves the next one's copy). A repository where the person cannot delete
  keeps its copy, and the confirm says which and why. onConfirm gets the deletes to run.

    <GroupDeleteDialog bind:open repo={group} targets={[target]} onConfirm={(deletes) => run(deletes)} />
-->
<script lang="ts">
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import { api, errorMessage, type DeletePlan, type FileHolders, type Repo } from "$lib/api";
  import type { GroupDelete } from "$lib/deletes";
  import { count, someNames } from "$lib/words";
  import { baseName } from "../browse";
  import type { DeleteTarget } from "../files/FolderList.svelte";

  let {
    open = $bindable(false),
    repo,
    targets,
    onConfirm,
  }: {
    open?: boolean;
    /** The group. */
    repo: Repo;
    targets: DeleteTarget[];
    onConfirm: (deletes: GroupDelete[]) => unknown;
  } = $props();

  const id = $props.id();
  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;
  const batch = 1000;

  type Item = { path: string; dir: boolean; repos: string[] };

  let holders = $state<FileHolders | null>(null);
  let holdersError = $state("");
  /** The plan of the one folder in each repository that can delete it, or why it failed. */
  let plans = $state<Record<string, DeletePlan | string>>({});
  let choice = $state("");
  let seq = 0;

  const paths = $derived(targets.flatMap((t) => t.files ?? (t.dir ? [] : [t.path])));
  const prefixes = $derived(targets.filter((t) => t.dir).map((t) => t.path));
  const oneFolder = $derived(targets.length === 1 && targets[0].dir ? targets[0].path : "");

  /** The holders of every item, 1000 items a request (the most the server takes). */
  async function loadHolders(group: string, ps: string[], xs: string[]): Promise<FileHolders> {
    const out: FileHolders = { members: [], paths: [], prefixes: [] };
    const all = [...ps.map((p) => ({ p, dir: false })), ...xs.map((p) => ({ p, dir: true }))];
    for (let i = 0; i < all.length; i += batch) {
      const part = all.slice(i, i + batch);
      const h = await api.repos.fileHolders(group, { paths: part.filter((x) => !x.dir).map((x) => x.p), prefixes: part.filter((x) => x.dir).map((x) => x.p) });
      for (const m of h.members) if (!out.members.some((o) => o.repo === m.repo)) out.members.push(m);
      out.paths.push(...h.paths);
      out.prefixes.push(...h.prefixes);
    }
    return out;
  }

  $effect(() => {
    const ps = paths;
    const xs = prefixes;
    const folder = oneFolder;
    const group = repo.name;
    const mine = ++seq;
    holders = null;
    holdersError = "";
    plans = {};
    choice = "";
    if (!open || ps.length + xs.length === 0) return;
    loadHolders(group, ps, xs).then(
      (h) => {
        if (mine !== seq) return;
        holders = h;
        if (!folder) return;
        const can = new Set(h.members.filter((m) => m.delete).map((m) => m.repo));
        for (const m of h.prefixes[0]?.repos ?? []) {
          if (!can.has(m)) continue;
          api.repos.deletePlan(m, { prefixes: [folder] }).then(
            (p) => {
              if (mine === seq) plans = { ...plans, [m]: p };
            },
            (e) => {
              if (mine === seq) plans = { ...plans, [m]: errorMessage(e) };
            },
          );
        }
      },
      (e) => {
        if (mine === seq) holdersError = errorMessage(e);
      },
    );
  });

  const canDelete = $derived(new Set((holders?.members ?? []).filter((m) => m.delete).map((m) => m.repo)));
  const items = $derived<Item[]>(
    holders ? [...holders.paths.map((p) => ({ ...p, dir: false })), ...holders.prefixes.map((p) => ({ ...p, dir: true }))] : [],
  );
  const held = $derived(items.filter((i) => i.repos.length > 0));
  /** The repositories that can delete a picked item, in read order. */
  const all = $derived((holders?.members ?? []).filter((m) => m.delete).map((m) => m.repo));
  const kept = $derived((holders?.members ?? []).filter((m) => !m.delete));
  /** The repositories that serve a picked item and can delete it. */
  const serving = $derived([...new Set(held.map((i) => i.repos[0]).filter((r) => canDelete.has(r)))]);
  // A choice when deleting everywhere deletes more than deleting where each item is served.
  const choose = $derived(serving.length > 0 && held.some((i) => i.repos.filter((r) => canDelete.has(r)).length > (canDelete.has(i.repos[0]) ? 1 : 0)));
  const how = $derived(choose ? choice : "all");

  /** The deletes of the choice: everywhere in one request, or each repository its own items. */
  const deletes = $derived.by((): GroupDelete[] => {
    const target = (is: Item[]) => ({ paths: is.filter((i) => !i.dir).map((i) => i.path), prefixes: is.filter((i) => i.dir).map((i) => i.path) });
    if (how === "all") {
      const is = held.filter((i) => i.repos.some((r) => canDelete.has(r)));
      return is.length ? [{ target: target(is), members: all }] : [];
    }
    if (how === "serving") return serving.map((m) => ({ target: target(held.filter((i) => i.repos[0] === m)), members: [m] }));
    return [];
  });
  /** The repositories the delete goes to, in read order. */
  const where = $derived(all.filter((r) => deletes.some((d) => d.members.includes(r))));

  const single = $derived(targets.length === 1 ? targets[0] : null);
  const manyFiles = $derived(targets.filter((t) => !t.dir).length);
  const manyFolders = $derived(targets.length - manyFiles);
  const manyNoun = $derived(
    [manyFiles && count(manyFiles, "file", "files"), manyFolders && count(manyFolders, "folder", "folders")].filter(Boolean).join(" and "),
  );

  /** The files the folder delete removes in the repositories it goes to, once every plan is in. */
  const folderFiles = $derived.by(() => {
    let n = 0;
    for (const r of where) {
      const p = plans[r];
      if (!p || typeof p === "string" || p.more) return null;
      n += p.files;
    }
    return n;
  });

  const changes = $derived.by(() => {
    if (holdersError) return [`The repositories that hold it could not be found: ${holdersError}`];
    if (!holders) return ["Finding the repositories that hold it…"];
    if (held.length === 0) return ["No repository of the group holds it any more"];
    const out: string[] = [];
    const keeps = kept.map((m) => `${m.repo} keeps its copy${m.reason ? `: ${m.reason}` : ""}`);
    if (all.length === 0) return ["You cannot delete it in the repositories that hold it", ...keeps];
    if (choose && !how) {
      out.push("Choose where to delete it");
    } else if (single?.files) {
      out.push(`Deletes ${nFiles(single.files.length)} of version ${single.version} from ${someNames(where)}`);
    } else if (single && !single.dir) {
      out.push(`Deletes ${single.path} from ${someNames(where)}`);
    } else if (single) {
      const notes = new Map<string, { count: number; one: string; many: string }>();
      for (const r of where) {
        const p = plans[r];
        if (p === undefined) out.push(`Deletes every file under ${single.path} in ${r} (counting the files…)`);
        else if (typeof p === "string") out.push(`Deletes every file under ${single.path} in ${r} (the files could not be counted: ${p})`);
        else out.push(`Deletes ${p.more ? "more than " : ""}${nFiles(p.files)} under ${single.path} in ${r}`);
        if (p && typeof p !== "string") {
          for (const n of p.notes) {
            const sum = notes.get(n.text);
            notes.set(n.text, { count: (sum?.count ?? 0) + n.count, one: n.one, many: n.many });
          }
        }
      }
      for (const [text, n] of notes) out.push(`${n.count.toLocaleString()} ${n.count === 1 ? n.one : n.many}: ${text}`);
    } else {
      out.push(`Deletes the ${manyNoun} from ${someNames(where)}`);
      if (manyFolders) out.push(`Deletes every file under the ${manyFolders === 1 ? "folder" : "folders"}`);
    }
    out.push(...keeps);
    out.push(how === "serving" ? "Clients of the group get the copy of the next repository that holds it, else “not found”" : "Clients that still need these files get “not found”");
    return out;
  });

  const title = $derived(
    single ? (single.files ? `Delete version ${single.version}?` : single.dir ? "Delete this folder?" : "Delete this file?") : `Delete ${manyNoun}?`,
  );
  const confirmLabel = $derived.by(() => {
    if (!single) return `Delete ${manyNoun}`;
    if (single.files) return `Delete ${nFiles(single.files.length)}`;
    if (!single.dir) return "Delete file";
    if (folderFiles) return `Delete ${nFiles(folderFiles)}`;
    return "Delete folder";
  });
  const servingLabel = $derived(serving.length === 1 ? `Only from ${serving[0]}` : "Only from the repository that serves each one");
</script>

<ConfirmDialog
  bind:open
  {title}
  description={single ? "You cannot undo this." : `${someNames(targets.map((t) => baseName(t.path)))}. You cannot undo this.`}
  {changes}
  {confirmLabel}
  destructive
  disabled={deletes.length === 0}
  onConfirm={() => onConfirm(deletes)}
>
  {#if choose}
    <RadioGroup.Root class="gap-1" aria-label="Where to delete" bind:value={choice}>
      <OptionRow id="{id}-all" label={all.length === 1 ? `Delete from ${all[0]}` : `Delete from all ${all.length} repositories`} description={all.length > 1 ? someNames(all) : undefined}>
        {#snippet control()}<RadioGroup.Item id="{id}-all" value="all" />{/snippet}
      </OptionRow>
      <OptionRow id="{id}-serving" label={servingLabel}>
        {#snippet control()}<RadioGroup.Item id="{id}-serving" value="serving" />{/snippet}
      </OptionRow>
    </RadioGroup.Root>
  {/if}
</ConfirmDialog>
