<!--
  The Settings area: header, a left sidebar by category (src/lib/nav.ts), and the page. Admin
  categories show only to admins; a setup-only session sees only the second-factor page. On
  narrow screens the sidebar is a sheet that the header button opens.
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import * as Sidebar from "$lib/components/ui/sidebar";
  import * as Alert from "$lib/components/ui/alert";
  import ShieldAlertIcon from "@lucide/svelte/icons/shield-alert";
  import AppHeader from "./AppHeader.svelte";
  import SettingsNav from "./SettingsNav.svelte";
  import { auth } from "$lib/auth.svelte";

  let { children }: { children: Snippet } = $props();
</script>

<Sidebar.Provider class="flex-col" style="--sidebar-width: 15rem;">
  <AppHeader wide>
    {#snippet start()}
      <Sidebar.Trigger class="-ml-1 md:hidden" aria-label="Open the settings menu" />
    {/snippet}
  </AppHeader>
  <div class="flex flex-1">
    <SettingsNav />
    <Sidebar.Inset>
      <div id="main" class="mx-auto w-full max-w-page px-4 py-8 sm:px-8 sm:py-10">
        {#if auth.setupOnly}
          <Alert.Root class="mb-8">
            <ShieldAlertIcon />
            <Alert.Title>Set up two-step verification to continue</Alert.Title>
            <Alert.Description>
              Your account needs an authenticator app or a passkey. Until you add one, only this page opens.
            </Alert.Description>
          </Alert.Root>
        {/if}
        {@render children()}
      </div>
    </Sidebar.Inset>
  </div>
</Sidebar.Provider>
