<!--
  One quiet notice in the app shell for a person who may change the general settings: the app is
  open at an address (location.origin) that is not the server's base_url. Use saves this address;
  Keep hides the notice for this address in this browser. It never shows under the Vite dev server,
  at a localhost address, to a setup-only session, or when an environment variable sets base_url.
  It checks again after the person leaves Settings › General, where base_url is edited.
-->
<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import { toast } from "svelte-sonner";
  import { api } from "$lib/api";
  import { auth } from "$lib/auth.svelte";
  import { router } from "$lib/router.svelte";

  const origin = location.origin;
  const keepKey = `telesphorepo.address.keep.${origin}`;

  /** The saved base_url when it differs from origin; null hides the notice. */
  let saved = $state<string | null>(null);
  let saving = $state(false);

  function loopback(host: string): boolean {
    const h = host.toLowerCase().replace(/^\[|\]$/g, "");
    return h === "localhost" || h.endsWith(".localhost") || /^127\./.test(h) || h === "::1" || h === "0.0.0.0" || h === "::";
  }

  function kept(): boolean {
    try {
      return localStorage.getItem(keepKey) === "1";
    } catch {
      return false;
    }
  }

  const eligible = $derived(
    !import.meta.env.DEV && !loopback(location.hostname) && !!auth.user && !auth.setupOnly && auth.can("settings.general"),
  );
  const onGeneral = $derived(router.path === "/settings/general");

  $effect(() => {
    if (!eligible || onGeneral || kept()) {
      saved = null;
      return;
    }
    let live = true;
    api.settings.get().then(
      (s) => {
        const item = s.groups.flatMap((g) => g.settings).find((x) => x.key === "base_url");
        const value = typeof item?.value === "string" ? item.value : "";
        if (live) saved = item && !item.locked && value !== origin ? value : null;
      },
      () => live && (saved = null),
    );
    return () => (live = false);
  });

  async function use() {
    saving = true;
    try {
      await api.settings.patch({ base_url: origin });
      saved = null;
      toast.success(`Set the server's address to ${origin}`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : String(e));
    } finally {
      saving = false;
    }
  }

  function keep() {
    try {
      localStorage.setItem(keepKey, "1");
    } catch {
      /* the notice comes back next time */
    }
    saved = null;
  }
</script>

{#if saved !== null}
  <Alert.Root role="status" class="mb-6">
    <GlobeIcon />
    <Alert.Description class="text-foreground">
      {#if saved}
        This server's address is set to {saved}, but you opened it at {origin}. Clients and snippets use the set address.
      {:else}
        This server has no address set, and you opened it at {origin}. Clients and snippets use the address of each request until one is set.
      {/if}
      <div class="mt-3 flex flex-wrap gap-2">
        <Button size="sm" variant="outline" disabled={saving} onclick={use}>Use {origin}</Button>
        <Button size="sm" variant="ghost" disabled={saving} onclick={keep}>Keep</Button>
      </div>
    </Alert.Description>
  </Alert.Root>
{/if}
