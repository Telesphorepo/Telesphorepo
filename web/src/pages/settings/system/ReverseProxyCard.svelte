<!--
  Server settings, "Reverse proxy": the trusted_proxies setting in plain words (spec §13.1). The
  server takes the visitor's address from X-Forwarded-For only when the connection comes from a
  trusted proxy. The presets "private" and "cloudflare" are checkboxes; other proxies are
  addresses or ranges, one per line. The default trusts no proxy. An untrusted peer that sent
  forwarded headers (GET /api/network/proxy-candidates) shows a banner with "Trust this address".

    <ReverseProxyCard setting={trustedProxies} baseURL={baseURL} onSaved={(s) => ...} />
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, ApiError, errorMessage, type Setting, type Settings } from "$lib/api";
  import { checkRange } from "$lib/network";
  import { Resource } from "$lib/load.svelte";
  import { formatCount, formatRelative } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Label } from "$lib/components/ui/label";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Badge } from "$lib/components/ui/badge";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import CopyField from "$lib/components/CopyField.svelte";

  let {
    setting,
    baseURL = "",
    onSaved,
  }: {
    /** The trusted_proxies setting from GET /api/settings. */
    setting: Setting;
    /** The server's public URL, for the Caddyfile example. */
    baseURL?: string;
    onSaved: (result: Settings) => void;
  } = $props();

  const id = $props.id();
  const presetNames = ["private", "cloudflare"];

  const listOf = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : []);

  let usePrivate = $state(false);
  let useCloudflare = $state(false);
  let others = $state("");

  function fill(s: Setting) {
    const l = listOf(s.value);
    usePrivate = l.includes("private");
    useCloudflare = l.includes("cloudflare");
    others = l.filter((x) => !presetNames.includes(x)).join("\n");
  }
  // Show the stored value again whenever the page loads new settings.
  $effect(() => {
    const s = setting;
    untrack(() => fill(s));
  });

  const ro = $derived(setting.locked || !!setting.read_only);
  const otherLines = $derived(others.split(/[,\n]/).map((x) => x.trim()).filter(Boolean));
  const lineErrors = $derived(otherLines.map(checkRange).filter(Boolean));
  const next = $derived([...(usePrivate ? ["private"] : []), ...(useCloudflare ? ["cloudflare"] : []), ...otherLines]);
  const dirty = $derived(JSON.stringify(next) !== JSON.stringify(listOf(setting.value)));
  const isDefault = $derived(setting.source === "default");
  const defaultList = $derived(listOf(setting.default));

  const words = (l: string[]) =>
    l.map((x) => (x === "private" ? "Private networks" : x === "cloudflare" ? "Cloudflare" : x)).join(", ");

  const host = $derived.by(() => {
    try {
      return new URL(baseURL).host || "repo.example.com";
    } catch {
      return "repo.example.com";
    }
  });
  const caddyfile = $derived(`${host} {\n\treverse_proxy localhost:8080\n}`);

  // --- save ---

  let confirmOpen = $state(false);
  let resetOpen = $state(false);

  const effects = [
    "Changes the visitor address that network rules, sign-in limits and the activity log use",
  ];
  const changes = $derived([
    next.length
      ? `Takes the visitor's address from these proxies: ${words(next)}`
      : "Trusts no proxy: the server uses the address of the direct connection only",
    ...effects,
  ]);

  // --- proxy candidates: untrusted peers that sent forwarded headers ---

  const candidates = new Resource(() => api.network.proxyCandidates());
  let trustAddress = $state("");
  let trustOpen = $state(false);
  function askTrust(address: string) {
    trustAddress = address;
    trustOpen = true;
  }

  /** Saves the setting (null resets it); an error stays in the confirm dialog. */
  async function patch(value: string[] | null) {
    try {
      const result = (await api.settings.patch({ [setting.key]: value })) as Settings;
      toast.success("Saved the reverse proxy settings");
      candidates.reload();
      onSaved(result);
    } catch (e) {
      throw new Error(e instanceof ApiError && e.fields?.[setting.key] ? e.fields[setting.key] : errorMessage(e));
    }
  }
</script>

<form
  class="flex flex-col gap-6"
  autocomplete="off"
  onsubmit={(e) => {
    e.preventDefault();
    if (dirty && lineErrors.length === 0) confirmOpen = true;
  }}
>
  <div class="flex flex-col gap-3 text-sm">
    <p class="max-w-prose text-muted-foreground">
      A reverse proxy, such as Caddy, takes the connections from visitors and passes them on to this server. The proxy
      sends each visitor's address in a header (X-Forwarded-For). The server believes that header only from the proxies
      you trust here, because anyone else could send a false address to get around network rules.
    </p>
    <p class="max-w-prose text-muted-foreground">
      By default the server trusts no proxy. When a request with a forwarded address arrives from an address that is not
      trusted, the server shows that address here, so you can trust your proxy with one click.
    </p>
    {#if setting.locked}
      <div>
        <Badge variant="secondary" class="font-mono" title="Remove the environment variable {setting.env} to change this here.">
          Set by {setting.env}
        </Badge>
      </div>
    {/if}
  </div>

  {#each candidates.data ?? [] as c (c.address)}
    <div class="flex max-w-form flex-col gap-3 rounded-xl bg-muted/60 px-4 py-3 text-sm">
      <p class="font-medium">
        Requests from <span class="font-mono">{c.address}</span> carry forwarded headers. Is this your reverse proxy?
      </p>
      <p class="text-muted-foreground">
        {c.header} seen {formatCount(c.count)} {c.count === 1 ? "time" : "times"}, last {formatRelative(c.last_seen)}.
        Trust it only if this is the address of your proxy. Anyone who can send requests from this address can then
        choose the visitor address that network rules and the activity log see.
      </p>
      {#if ro}
        <p class="text-muted-foreground">
          {setting.env} sets the trusted proxies. Add <span class="font-mono">{c.address}</span> there.
        </p>
      {:else}
        <div>
          <Button type="button" size="sm" onclick={() => askTrust(c.address)}>Trust this address</Button>
        </div>
      {/if}
    </div>
  {/each}

  <fieldset class="flex flex-col gap-1">
    <legend class="mb-2 text-sm font-semibold">Trusted proxies</legend>
    <div class="flex items-start gap-3 rounded-lg px-2 py-2 hover:bg-hover has-data-[state=checked]:bg-selected has-data-[state=checked]:text-selected-foreground">
      <Checkbox id="{id}-private" class="mt-0.5" checked={usePrivate} disabled={ro} onCheckedChange={(v) => (usePrivate = v === true)} />
      <div class="flex min-w-0 flex-col gap-1">
        <Label for="{id}-private">Private networks</Label>
        <p class="text-sm text-muted-foreground">
          Any proxy on this machine or in your own network: 127.0.0.1, 10.x.x.x, 172.16.x.x to 172.31.x.x, 192.168.x.x and
          their IPv6 equivalents.
        </p>
        <p class="text-sm text-muted-foreground">
          Turn this on only when nobody can reach the server's port without the proxy. With Docker port publishing or
          rootless Podman, every outside connection looks private, so anyone could send a false address. Trust the proxy's
          own address instead.
        </p>
      </div>
    </div>
    <div class="flex items-start gap-3 rounded-lg px-2 py-2 hover:bg-hover has-data-[state=checked]:bg-selected has-data-[state=checked]:text-selected-foreground">
      <Checkbox id="{id}-cloudflare" class="mt-0.5" checked={useCloudflare} disabled={ro} onCheckedChange={(v) => (useCloudflare = v === true)} />
      <div class="flex min-w-0 flex-col gap-1">
        <Label for="{id}-cloudflare">Cloudflare</Label>
        <p class="text-sm text-muted-foreground">
          Turn this on when visitors reach the server through Cloudflare's proxy (the orange cloud). The server uses
          Cloudflare's list of its own addresses, which it downloads every day.
        </p>
      </div>
    </div>
  </fieldset>

  <div class="flex flex-col gap-2">
    <Label for="{id}-others">Other proxies</Label>
    <Textarea
      id="{id}-others"
      class="max-w-form font-mono text-[0.8125rem]"
      rows={Math.min(8, Math.max(2, others.split("\n").length + 1))}
      placeholder={"10.0.0.5\n203.0.113.0/24"}
      spellcheck={false}
      disabled={ro}
      aria-invalid={lineErrors.length ? true : undefined}
      bind:value={others}
    />
    <p class="text-sm text-muted-foreground">Addresses or ranges of other proxies, one per line.</p>
    {#each lineErrors as err (err)}<ErrorState inline error={err} />{/each}
  </div>

  {#if next.length === 0}
    <p class="rounded-xl bg-muted/60 px-4 py-3 text-sm">
      No proxy is trusted. The server uses the address of the direct connection. Behind a proxy, every visitor then has
      the proxy's address.
    </p>
  {/if}

  {#if !ro}
    <div class="flex flex-wrap items-center gap-2">
      <Button type="submit" disabled={!dirty || lineErrors.length > 0}>Save changes</Button>
      {#if dirty}<Button type="button" variant="ghost" onclick={() => fill(setting)}>Discard edits</Button>{/if}
      {#if !isDefault && !dirty}
        <Button type="button" variant="outline" onclick={() => (resetOpen = true)}>Reset to the default</Button>
      {/if}
    </div>
  {/if}

  <div class="flex flex-col gap-2">
    <h3 class="text-sm font-semibold">Caddy</h3>
    <p class="max-w-prose text-sm text-muted-foreground">
      We recommend Caddy as the reverse proxy: it gets HTTPS certificates on its own and sends X-Forwarded-For without
      extra settings. This Caddyfile is all it needs when Caddy runs on the same machine:
    </p>
    <CopyField label="Caddyfile" value={caddyfile} block />
    <p class="max-w-prose text-sm text-muted-foreground">
      If Cloudflare sits in front of Caddy, also tell Caddy to trust Cloudflare (the trusted_proxies server option in
      the Caddyfile). Otherwise Caddy replaces the visitor's address with Cloudflare's.
    </p>
  </div>
</form>

<ConfirmDialog bind:open={confirmOpen} title="Save reverse proxy settings" confirmLabel="Save changes" {changes} onConfirm={() => patch(next)} />

<ConfirmDialog
  bind:open={trustOpen}
  title="Trust this proxy"
  confirmLabel="Trust {trustAddress}"
  changes={[`Takes the visitor's address from ${trustAddress} (added to the trusted proxies)`, ...effects]}
  onConfirm={() => patch([...listOf(setting.value), trustAddress])}
/>

<ConfirmDialog
  bind:open={resetOpen}
  title="Reset the trusted proxies"
  confirmLabel="Reset to the default"
  changes={[
    defaultList.length ? `Takes the visitor's address from these proxies: ${words(defaultList)}` : "Trusts no proxy",
    ...effects,
  ]}
  onConfirm={() => patch(null)}
/>
