<!--
  The shell: loads the user, matches the hash route, checks access, and renders the page inside
  its layout. Pages never check access themselves.
-->
<script lang="ts">
  import { ModeWatcher } from "mode-watcher";
  import { Toaster } from "$lib/components/ui/sonner";
  import { auth } from "$lib/auth.svelte";
  import { match, navigate, router } from "$lib/router.svelte";
  import { setupOnlyPaths } from "$lib/nav";
  import { routes, redirects } from "./routes";
  import BrowseLayout from "$lib/layouts/BrowseLayout.svelte";
  import SettingsLayout from "$lib/layouts/SettingsLayout.svelte";
  import BareLayout from "$lib/layouts/BareLayout.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Button } from "$lib/components/ui/button";
  import ShieldIcon from "@lucide/svelte/icons/shield";
  import CompassIcon from "@lucide/svelte/icons/compass";

  auth.load();

  const matched = $derived(match(routes, router.path));

  // Redirects, sign-in for user pages, and the setup-only session.
  $effect(() => {
    if (!auth.ready) return;
    const to = redirects[router.path];
    if (to) {
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
  const Layout = $derived(
    matched?.route.layout === "settings" ? SettingsLayout : matched?.route.layout === "bare" ? BareLayout : BrowseLayout,
  );
  // Load the page module only when the route changes, not on every path change within it (a page
  // keeps its state while its params change, e.g. folders of one repo).
  const route = $derived(matched?.route);
  const page = $derived(route?.page());
</script>

<ModeWatcher />
<Toaster position="bottom-right" />

{#if auth.ready}
  <Layout>
    {#if !matched}
      <EmptyState icon={CompassIcon} title="This page does not exist" description="Check the address, or start from the repositories.">
        <Button href="#/">Open repositories</Button>
      </EmptyState>
    {:else if forbidden}
      <EmptyState icon={ShieldIcon} title="Admins only" description="Ask an administrator if you need this page." />
    {:else if matched.route.access === "any" || auth.user}
      {#await page}
        <LoadingRows rows={4} />
      {:then mod}
        {#if mod}
          {@const Page = mod.default}
          <Page params={matched.params} query={router.query} />
        {/if}
      {:catch e}
        <ErrorState error={e instanceof Error ? e.message : String(e)} title="The page did not load" onRetry={() => location.reload()} />
      {/await}
    {/if}
  </Layout>
{/if}
