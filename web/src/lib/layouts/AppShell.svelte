<!--
  The one app shell: the top bar, then one centered container (max 1200px, 24px sides, 16px
  under 640px) for every page. `settings` adds the 220px Settings sidebar inside the container,
  40px from the content column; every Settings page uses that whole column. The sidebar sticks under
  the top bar and scrolls on its own when it is taller than the window. Under 768px the sidebar is
  a menu that the top bar's menu button opens (the only side sheet).
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as Sheet from "$lib/components/ui/sheet";
  import * as Alert from "$lib/components/ui/alert";
  import ShieldAlertIcon from "@lucide/svelte/icons/shield-alert";
  import TopBar from "./TopBar.svelte";
  import SettingsNav from "./SettingsNav.svelte";
  import { auth } from "$lib/auth.svelte";
  import { router } from "$lib/router.svelte";

  let { settings = false, children }: { settings?: boolean; children: Snippet } = $props();

  let menuOpen = $state(false);

  // Close the menu after a navigation.
  $effect(() => {
    void router.path;
    menuOpen = false;
  });
</script>

<div class="flex min-h-svh flex-col">
  <TopBar onMenu={settings ? () => (menuOpen = true) : undefined} />
  <div class="mx-auto w-full max-w-page flex-1 px-4 pt-8 pb-16 sm:px-6">
    {#if settings}
      <div class="flex gap-10">
        <aside class="hidden w-[220px] shrink-0 md:block">
          <div class="sticky top-22 -mx-1 max-h-[calc(100svh-6.5rem)] overflow-y-auto px-1 pb-4"><SettingsNav /></div>
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
          <Sheet.Title class="sr-only">Settings menu</Sheet.Title>
          <SettingsNav />
        </Sheet.Content>
      </Sheet.Root>
    {:else}
      <main id="main">{@render children()}</main>
    {/if}
  </div>
</div>
