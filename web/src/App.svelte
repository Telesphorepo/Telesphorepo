<!--
  The shell: loads the user, matches the hash route, checks access, and renders the page inside
  the app shell. Pages never check the route access themselves (a repository page checks the
  caller's permission on that repository).
-->
<script lang="ts">
  import { ModeWatcher } from "mode-watcher";
  import { Toaster } from "$lib/components/ui/sonner";
  import * as Tooltip from "$lib/components/ui/tooltip";
  import { auth } from "$lib/auth.svelte";
  import type { Component } from "svelte";
  import { navigate, router, type PageProps, type RouteDef } from "$lib/router.svelte";
  import { current } from "$lib/current.svelte";
  import { setupOnlyPaths } from "$lib/nav";
  import AppShell from "$lib/layouts/AppShell.svelte";
  import UnsavedGuard from "$lib/layouts/UnsavedGuard.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Button } from "$lib/components/ui/button";
  import ShieldIcon from "@lucide/svelte/icons/shield";
  import CompassIcon from "@lucide/svelte/icons/compass";

  auth.load();

  const matched = $derived(current.match);

  // Redirects, sign-in for user pages, and the setup-only session.
  $effect(() => {
    if (!auth.ready) return;
    const to = current.redirect;
    if (to && to !== router.path) {
      navigate(to, { query: Object.fromEntries(router.query), replace: true });
      return;
    }
    const r = matched?.route;
    if (r && r.access !== "any" && !auth.user) {
      navigate("/login", { query: { next: router.path + (router.search ? "?" + router.search : "") }, replace: true });
      return;
    }
    if (auth.setupOnly && r && r.access !== "any" && !setupOnlyPaths.includes(router.path)) {
      navigate(setupOnlyPaths[0], { replace: true });
    }
  });

  $effect(() => {
    document.title = matched ? `${matched.route.title} · Telesphorepo` : "Telesphorepo";
  });

  const forbidden = $derived(!!matched && matched.route.access === "admin" && !!auth.user && !auth.isAdmin);
  // Load the page module only when the route changes, not on every path change within it (a page
  // keeps its state while its params change, e.g. folders of one repo). A page renders only while
  // its own route matches, so the page that is leaving never gets the params of the next route.
  const route = $derived(matched?.route);
  let loaded = $state.raw<{ route: RouteDef; Page: Component<PageProps> } | null>(null);
  let loadError = $state("");
  $effect(() => {
    const r = route;
    if (!r) return;
    let live = true;
    loadError = "";
    r.page().then(
      (mod) => live && (loaded = { route: r, Page: mod.default }),
      (e) => live && (loadError = e instanceof Error ? e.message : String(e)),
    );
    return () => (live = false);
  });
</script>

<ModeWatcher />
<Toaster position="bottom-right" />
<UnsavedGuard />

<Tooltip.Provider delayDuration={400}>
  {#if auth.ready}
    <AppShell settings={matched?.route.layout === "settings"}>
      {#if current.redirect}
        <Loading rows={4} />
      {:else if !matched}
        <EmptyState icon={CompassIcon} title="This page does not exist" description="Check the address, or start from the repositories.">
          <Button href="#/">Open repositories</Button>
        </EmptyState>
      {:else if forbidden}
        <EmptyState icon={ShieldIcon} title="Administrators only" description="Ask an administrator if you need this page." />
      {:else if matched.route.access === "any" || auth.user}
        {#if loadError}
          <ErrorState error={loadError} title="The page did not load" onRetry={() => location.reload()} />
        {:else if loaded && loaded.route === matched.route}
          <loaded.Page params={matched.params} query={router.query} />
        {:else}
          <Loading rows={4} />
        {/if}
      {/if}
    </AppShell>
  {/if}
</Tooltip.Provider>
