<!--
  The one app shell: the top bar, then one centered container (max 1440px, 24px sides, 16px
  under 640px) for every page. `sidebar` adds the 240px sidebar of Settings or of Your account
  inside the container, 40px from the content column; every page with a sidebar uses that whole
  column. The sidebar sticks under the top bar and scrolls on its own when it is taller than the
  window. Under 768px the sidebar is a menu that the top bar's menu button opens (the only side
  sheet). The address notice (AddressNotice) sits at the top of the container. The container names
  its layout (data-layout="browse" or "settings", the second for both sidebars), which sets the
  row height and the space between blocks (app.css): Browse pages are airier than Settings pages.
  Under the container, on every page, one quiet line: "Powered by Telesphorepo".
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import type { SidebarArea } from "../../modules/types";
  import * as Sheet from "$lib/components/ui/sheet";
  import * as Alert from "$lib/components/ui/alert";
  import ShieldAlertIcon from "@lucide/svelte/icons/shield-alert";
  import TopBar from "./TopBar.svelte";
  import SettingsNav from "./SettingsNav.svelte";
  import AddressNotice from "./AddressNotice.svelte";
  import { auth } from "$lib/auth.svelte";
  import { router } from "$lib/router.svelte";
  import { productName } from "$lib/brand.svelte";

  let { sidebar, children }: { sidebar?: SidebarArea; children: Snippet } = $props();

  let menuOpen = $state(false);

  // Close the menu after a navigation.
  $effect(() => {
    void router.path;
    menuOpen = false;
  });
</script>

<div class="flex min-h-svh flex-col">
  <TopBar {sidebar} onMenu={sidebar ? () => (menuOpen = true) : undefined} />
  <div data-layout={sidebar ? "settings" : "browse"} class="mx-auto w-full max-w-page flex-1 px-4 pt-10 pb-20 sm:px-6">
    <AddressNotice />
    {#if sidebar}
      <div class="flex gap-10">
        <aside class="hidden w-60 shrink-0 md:block">
          <div class="sticky top-22 -mx-1 max-h-[calc(100svh-6.5rem)] overflow-y-auto px-1 pb-4"><SettingsNav area={sidebar} /></div>
        </aside>
        <main id="main" class="min-w-0 flex-1">
          {#if auth.setupOnly}
            <Alert.Root class="mb-6">
              <ShieldAlertIcon />
              <Alert.Title>Set up two-step verification to continue</Alert.Title>
              <Alert.Description>
                Your account needs an authenticator app or a passkey. Until you add one, only this page opens.
              </Alert.Description>
            </Alert.Root>
          {/if}
          {@render children()}
        </main>
      </div>
      <Sheet.Root bind:open={menuOpen}>
        <Sheet.Content side="left" class="overflow-y-auto p-4 pt-14 data-[side=left]:w-72">
          <Sheet.Title class="sr-only">{sidebar === "account" ? "Your account menu" : "Settings menu"}</Sheet.Title>
          <SettingsNav area={sidebar} />
        </Sheet.Content>
      </Sheet.Root>
    {:else}
      <main id="main">{@render children()}</main>
    {/if}
  </div>
  <footer class="mx-auto w-full max-w-page px-4 pb-6 text-center text-meta text-muted-foreground sm:px-6">Powered by {productName}</footer>
</div>
