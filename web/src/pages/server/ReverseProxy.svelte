<!--
  Network and proxy (Network permission): the trusted proxies only (the trusted_proxies setting, in
  plain words). The server takes the visitor's address from X-Forwarded-For only when the
  connection comes from a trusted proxy: none, a proxy on this machine or network ("private"),
  Cloudflare ("list:cloudflare") or custom addresses. Repository network limits live on each
  repository's Access tab and in Networks, not here. Proxies that sent forwarded headers without
  being trusted show as detected proxies, each with Trust. The Caddyfile is the recommended setup.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { api, ApiError, errorMessage, type ProxyCandidate, type Setting } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import RangePaste from "$lib/components/RangePaste.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
  import LearnMore from "$lib/components/LearnMore.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import WaypointsIcon from "@lucide/svelte/icons/waypoints";
  import XIcon from "@lucide/svelte/icons/x";

  let {}: PageProps = $props();

  const id = $props.id();
  const settings = new Resource(() => api.settings.get());
  const candidates = new Resource(() => api.network.proxyCandidates());

  const all = $derived((settings.data?.groups ?? []).flatMap((g) => g.settings));
  const setting = $derived<Setting | undefined>(all.find((s) => s.key === "trusted_proxies"));
  const baseURL = $derived(String(all.find((s) => s.key === "base_url")?.value ?? ""));
  const locked = $derived(!!setting?.locked);

  // The setting's entries, split by what they mean on this page. `lists` keeps the names of other
  // network lists that an older value may hold, so a save never drops them.
  type Value = { local: boolean; cloudflare: boolean; custom: string[]; lists: string[] };
  const split = (v: unknown): Value => {
    const xs = Array.isArray(v) ? v.map(String) : [];
    const named = xs.filter((x) => x.startsWith("list:")).map((x) => x.slice(5));
    return {
      local: xs.includes("private"),
      cloudflare: named.includes("cloudflare"),
      custom: xs.filter((x) => x !== "private" && !x.startsWith("list:")),
      lists: named.filter((n) => n !== "cloudflare"),
    };
  };
  const join = (v: Value): string[] => [
    ...(v.local ? ["private"] : []),
    ...(v.cloudflare ? ["list:cloudflare"] : []),
    ...v.lists.map((n) => "list:" + n),
    ...v.custom,
  ];

  let draft = $state<Draft<Value> | null>(null);
  $effect(() => {
    const v = setting?.value;
    if (setting) untrack(() => (draft = new Draft(split(v))));
  });

  // "Other addresses" opens when it holds addresses, or when the user ticks it.
  let customOpen = $state(false);
  const showCustom = $derived(customOpen || (draft?.value.custom.length ?? 0) > 0 || (draft?.value.lists.length ?? 0) > 0);
  function setCustom(on: boolean) {
    customOpen = on;
    if (!on && draft) {
      draft.value.custom = [];
      draft.value.lists = [];
    }
  }

  const addCustom = (r: string[]) => {
    if (draft) draft.value.custom = [...draft.value.custom, ...r.filter((x) => !draft!.value.custom.includes(x))];
  };
  function trust(c: ProxyCandidate) {
    if (!draft) return;
    customOpen = true;
    addCustom([c.address]);
  }

  // The custom entries as table rows: addresses, then other network lists.
  type Entry = { value: string; label: string; list: boolean };
  const entries = $derived<Entry[]>(
    draft ? [...draft.value.custom.map((a) => ({ value: a, label: a, list: false })), ...draft.value.lists.map((n) => ({ value: n, label: `Network list ${n}`, list: true }))] : [],
  );
  const entrySource = { get items() { return entries; }, loading: false, error: "" };
  function removeEntry(e: Entry) {
    if (!draft) return;
    if (e.list) draft.value.lists = draft.value.lists.filter((n) => n !== e.value);
    else draft.value.custom = draft.value.custom.filter((a) => a !== e.value);
  }

  const detected = $derived(candidates.data ?? []);

  // --- save: asks first, because every address rule and Activity depend on it ---
  let confirmOpen = $state(false);
  const changes = $derived.by(() => {
    if (!draft) return [];
    const v = draft.value;
    const was = draft.saved;
    const out: string[] = [];
    if (v.local !== was.local) out.push(v.local ? "Trusts a proxy on this machine or network" : "Stops trusting proxies on this machine or network");
    if (v.cloudflare !== was.cloudflare) out.push(v.cloudflare ? "Trusts Cloudflare" : "Stops trusting Cloudflare");
    const added = v.custom.filter((a) => !was.custom.includes(a));
    const removed = [...was.custom.filter((a) => !v.custom.includes(a)), ...was.lists.filter((n) => !v.lists.includes(n))];
    if (added.length) out.push(`Trusts ${added.join(", ")}`);
    if (removed.length) out.push(`Stops trusting ${removed.join(", ")}`);
    if (join(v).length === 0) out.push("Trusts no proxy: the server uses the address of the direct connection");
    out.push("Changes the visitor address that repository network limits, sign-in limits and Activity use");
    return out;
  });

  async function save() {
    if (!draft) return;
    try {
      await api.settings.patch({ trusted_proxies: join(draft.value) });
    } catch (e) {
      throw new Error(e instanceof ApiError && e.fields?.trusted_proxies ? e.fields.trusted_proxies : errorMessage(e));
    }
    draft.commit();
    customOpen = false;
    toast.success("Saved the trusted proxies");
    candidates.reload();
  }

  const host = $derived.by(() => {
    try {
      return new URL(baseURL).host || "repo.example.com";
    } catch {
      return "repo.example.com";
    }
  });
  const caddyfile = $derived(`${host} {\n\treverse_proxy localhost:8080\n}`);
  const quietDanger = "hover:bg-destructive/10 hover:text-destructive";
</script>

<PageHeader title="Network and proxy" description="How the server finds each visitor's address when a proxy sits in front of it." />

{#if settings.loading && !settings.data}
  <Loading rows={6} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if !setting || !draft}
  <ErrorState error="The server sent no trusted proxies setting." onRetry={settings.reload} />
{:else}
  {@const v = draft.value}
  <FormPage>
    {#if detected.length}
      <FormSection title="Detected proxies" description="These addresses sent forwarded headers, but the server does not trust them. Trust one only if it is your proxy.">
        <DataTable
          source={listOf(candidates)}
          caption="Detected proxies"
          key={(c) => c.address}
          columns={[
            { label: "Address", grow: true },
            { label: "Header", hideOnPhone: true },
            { label: "Requests", align: "right", hideOnPhone: true },
            { label: "Last seen", align: "right" },
            { label: "", srLabel: "Actions" },
          ]}
        >
          {#snippet row(c)}
            {@const added = v.custom.includes(c.address)}
            <Cell grow strong text={c.address} copy copyLabel="Copy {c.address}" />
            <Cell hideOnPhone muted text={c.header} />
            <Cell num hideOnPhone>{formatCount(c.count)}</Cell>
            <Cell num muted><RelativeTime at={c.last_seen} /></Cell>
            <Cell actions>
              {#if locked}
                <span class="text-muted-foreground">Add it to {setting.env}</span>
              {:else}
                <Button variant="outline" size="sm" disabled={added} onclick={() => trust(c)}>{added ? "Added" : "Trust"}</Button>
              {/if}
            </Cell>
          {/snippet}
        </DataTable>
      </FormSection>
    {/if}

    <FormSection
      title="Where your proxy is"
      description="The server believes a forwarded address only from the proxies chosen here, because anyone else could send a false one."
    >
      {#snippet actions()}
        {#if locked}<StatusBadge title="Remove the environment variable {setting.env} to change this here.">Set by {setting.env}</StatusBadge>{/if}
      {/snippet}
      <div class="flex flex-col gap-1">
        <OptionRow
          id="{id}-local"
          label="On this machine or in my network"
          description="Choose this for Caddy or nginx on the same machine or in a private network: 127.0.0.1, 10.x.x.x, 172.16.x.x to 172.31.x.x, 192.168.x.x and the IPv6 equivalents."
        >
          {#snippet control()}<Checkbox id="{id}-local" checked={v.local} disabled={locked} onCheckedChange={(c) => (v.local = c === true)} />{/snippet}
          <LearnMore label="When not to choose this">
            Choose it only when nobody can reach the server's port without the proxy. With Docker port publishing or rootless Podman, every
            outside connection looks private: trust the proxy's own address under Other addresses instead.
          </LearnMore>
        </OptionRow>
        <OptionRow
          id="{id}-cf"
          label="Cloudflare"
          description="The server trusts the addresses that Cloudflare publishes, and gets them again every day. It reads the visitor's address from the header that Cloudflare adds, also when a trusted proxy sits behind Cloudflare."
        >
          {#snippet control()}<Checkbox id="{id}-cf" checked={v.cloudflare} disabled={locked} onCheckedChange={(c) => (v.cloudflare = c === true)} />{/snippet}
        </OptionRow>
        <OptionRow id="{id}-custom" label="Other addresses" description="Choose this for a proxy at another address, such as a load balancer.">
          {#snippet control()}<Checkbox id="{id}-custom" checked={showCustom} disabled={locked} onCheckedChange={(c) => setCustom(c === true)} />{/snippet}
        </OptionRow>
      </div>

      {#if showCustom}
        <div class="mt-6 flex flex-col gap-6">
          <DataTable
            source={entrySource}
            caption="Other trusted proxies"
            key={(e) => e.value}
            columns={locked ? [{ label: "Address", grow: true }] : [{ label: "Address", grow: true }, { label: "", srLabel: "Remove" }]}
          >
            {#snippet row(e)}
              <Cell grow text={e.label} copy={!e.list && e.value} copyLabel="Copy {e.label}" />
              {#if !locked}
                <Cell actions>
                  <Button variant="ghost" size="icon-sm" class={quietDanger} aria-label="Remove {e.label}" onclick={() => removeEntry(e)}><XIcon /></Button>
                </Cell>
              {/if}
            {/snippet}
            {#snippet empty()}
              <CompactEmpty icon={WaypointsIcon} title="Other addresses" state="None yet" text="Paste the addresses of your proxies below." />
            {/snippet}
          </DataTable>
          {#if !locked}<RangePaste id="{id}-paste" label="Add proxy addresses" onAdd={addCustom} />{/if}
        </div>
      {/if}
    </FormSection>

    <FormSection
      title="Caddy"
      description="We recommend Caddy: it gets HTTPS certificates by itself and sends X-Forwarded-For without more settings. On this machine, this Caddyfile is all it needs."
    >
      <CodeBlock title="Caddyfile" code={caddyfile} />

    </FormSection>
  </FormPage>

  <SaveBar count={draft.count} onDiscard={() => { draft?.reset(); customOpen = false; }} onSave={() => (confirmOpen = true)} />

  <ConfirmDialog bind:open={confirmOpen} title="Save the trusted proxies?" confirmLabel="Save changes" {changes} onConfirm={save} />
{/if}
