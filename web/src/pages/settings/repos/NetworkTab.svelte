<!--
  Repository settings, "Network access" tab: the two IP allow-lists of the repo (spec §13.1).
  "Who can download" is the read list, "Who can publish" the publish list. An empty list lets
  every address through; the rules apply to admins too.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, type IPRule, type IPRules, type NetworkPreset, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { href } from "$lib/router.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import Section from "$lib/components/Section.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import IPRuleList, { errorsOf, type OfferedPreset } from "./IPRuleList.svelte";

  let { repo }: { repo: Repo } = $props();

  const stored = new Resource(() => api.repos.ipRules.get(repo.name));
  // Counts and refresh state of the fetched presets; the tab works without them.
  const fetched = new Resource(() => api.network.presets());

  const clone = (r: IPRules): IPRules => ({
    read: r.read.map((x) => ({ ...x })),
    publish: r.publish.map((x) => ({ ...x })),
  });
  // The edited lists; filled from stored.data once it loads.
  let draft = $state<IPRules>({ read: [], publish: [] });
  $effect(() => {
    const d = stored.data;
    if (d) untrack(() => (draft = clone(d)));
  });

  // What the PUT sends: trimmed values and notes.
  const tidy = (l: IPRule[]): IPRule[] => l.map((r) => ({ kind: r.kind, value: r.value.trim(), note: r.note.trim() }));
  const body = $derived({ read: tidy(draft.read), publish: tidy(draft.publish) });
  const dirty = $derived(
    !!stored.data && JSON.stringify(body) !== JSON.stringify({ read: tidy(stored.data.read), publish: tidy(stored.data.publish) }),
  );

  // --- presets offered for rules ---

  function fetchedInfo(p: NetworkPreset | undefined): string | undefined {
    if (!p) return undefined;
    if (p.count === 0)
      return `The server has not fetched the ${p.title} list yet, so it matches no address. Check "Network lists" in Server settings.`;
    return undefined;
  }

  const github = $derived(fetched.data?.find((p) => p.name === "github-actions"));
  const offered = $derived<OfferedPreset[]>([
    {
      name: "github-actions",
      title: "GitHub Actions",
      help:
        "The machines GitHub uses to run Actions jobs, from the list GitHub publishes" +
        (github?.count ? ` (${formatCount(github.count)} ranges, updated every day).` : ", updated every day.") +
        " Other GitHub users share these machines, so also set up a CI publishing rule.",
      warning: fetchedInfo(github),
    },
    {
      name: "private",
      title: "Private networks",
      help: "Addresses inside your own networks: this machine, 10.x.x.x, 172.16.x.x to 172.31.x.x, 192.168.x.x and their IPv6 equivalents.",
    },
  ]);

  const presetTitle = (name: string) => offered.find((p) => p.name === name)?.title ?? name;
  const describe = (l: IPRule[]) =>
    l.map((r) => (r.kind === "preset" ? presetTitle(r.value) : r.note ? `${r.value} (${r.note})` : r.value)).join(", ");

  // --- save ---

  let tried = $state(false);
  let confirmOpen = $state(false);

  const problems = $derived(
    [...errorsOf("Who can download", draft.read), ...errorsOf("Who can publish", draft.publish)],
  );

  function askSave() {
    tried = true;
    if (problems.length === 0) confirmOpen = true;
  }

  const changes = $derived.by(() => {
    if (!stored.data) return [];
    const out: string[] = [];
    const scopes = [
      { key: "read" as const, verb: "download from" },
      { key: "publish" as const, verb: "publish to" },
    ];
    for (const s of scopes) {
      const now = body[s.key];
      if (JSON.stringify(now) === JSON.stringify(tidy(stored.data[s.key]))) continue;
      if (now.length === 0) out.push(`Anyone with the right account can ${s.verb} ${repo.name} again, from any address`);
      else out.push(`Only requests from these can ${s.verb} ${repo.name}: ${describe(now)}`);
    }
    if (body.read.length || body.publish.length)
      out.push("Applies to admins too, and to apt, Maven and Gradle. A refused request is recorded in the activity log");
    return out;
  });

  async function save() {
    const result = await api.repos.ipRules.set(repo.name, body);
    stored.data = result;
    draft = clone(result);
    tried = false;
    toast.success(`Saved the network access of ${repo.name}`);
  }

  function discard() {
    if (stored.data) draft = clone(stored.data);
    tried = false;
  }
</script>

<Section
  narrow
  title="Network access"
  description="Limit which networks can reach this repository, on top of who has access. A request must come from an address on the list. Leave a list empty to allow every address."
>
  {#if stored.error && !stored.data}
    <ErrorState error={stored.error} title="The network rules did not load" onRetry={stored.reload} />
  {:else if !stored.data}
    <LoadingRows rows={4} />
  {:else}
    <form
      class="flex flex-col gap-8"
      autocomplete="off"
      onsubmit={(e) => {
        e.preventDefault();
        askSave();
      }}
    >
      <IPRuleList
        id="read"
        title="Who can download"
        description="Downloads, the file list, search and the Browse pages. If your own address is not on the list, you cannot browse this repository's files, but you can still change this list here."
        openText="Empty means anyone with the right account can download, from any address."
        presets={offered}
        bind:rules={draft.read}
        showErrors={tried}
      />
      <IPRuleList
        id="publish"
        title="Who can publish"
        description="Uploads, deletes, and new publish passwords from build tools, CI jobs and this site."
        openText="Empty means anyone with the right account can publish, from any address."
        presets={offered}
        bind:rules={draft.publish}
        showErrors={tried}
      />

      {#if tried && problems.length}
        <div class="flex flex-col gap-1">
          {#each problems as p (p)}<ErrorState inline error={p} />{/each}
        </div>
      {/if}

      <div class="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={!dirty}>Save changes</Button>
        {#if dirty}<Button type="button" variant="ghost" onclick={discard}>Discard edits</Button>{/if}
      </div>
    </form>

    <p class="mt-8 text-sm text-muted-foreground">
      Behind a reverse proxy, the server sees the visitor's address only if the proxy is trusted. Check
      <a class="text-link underline-offset-4 hover:underline" href={href("/settings/system")}>Reverse proxy in Server settings</a>.
    </p>
  {/if}
</Section>

<ConfirmDialog bind:open={confirmOpen} title="Save network access" confirmLabel="Save changes" {changes} onConfirm={save} />
