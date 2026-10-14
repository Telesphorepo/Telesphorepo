<!--
  The right end of the header. Signed in: an account menu (settings, theme, sign out). Signed out:
  "Sign in", which comes back to the current page, and a theme switch.
-->
<script lang="ts">
  import * as DropdownMenu from "$lib/components/ui/dropdown-menu";
  import * as Avatar from "$lib/components/ui/avatar";
  import { Button, buttonVariants } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { setMode, userPrefersMode, mode } from "mode-watcher";
  import SettingsIcon from "@lucide/svelte/icons/settings";
  import FolderIcon from "@lucide/svelte/icons/folder-open";
  import LogOutIcon from "@lucide/svelte/icons/log-out";
  import SunIcon from "@lucide/svelte/icons/sun";
  import MoonIcon from "@lucide/svelte/icons/moon";
  import { auth } from "$lib/auth.svelte";
  import { href, navigate, router } from "$lib/router.svelte";

  const initials = $derived((auth.user?.username ?? "?").slice(0, 2).toUpperCase());
  const next = $derived(router.path === "/login" ? undefined : router.path + (router.search ? "?" + router.search : ""));
</script>

{#if auth.user}
  <DropdownMenu.Root>
    <DropdownMenu.Trigger class={buttonVariants({ variant: "ghost", class: "h-9 gap-2 pr-2 pl-1" })} aria-label="Account menu">
      <Avatar.Root size="sm"><Avatar.Fallback class="text-[0.6875rem] font-semibold">{initials}</Avatar.Fallback></Avatar.Root>
      <span class="hidden max-w-40 truncate md:inline">{auth.user.username}</span>
    </DropdownMenu.Trigger>
    <DropdownMenu.Content align="end" class="w-60">
      <DropdownMenu.Label class="flex items-center justify-between gap-2 font-normal">
        <span class="min-w-0 truncate">
          Signed in as <span class="font-medium text-foreground">{auth.user.username}</span>
        </span>
        {#if auth.isAdmin}<Badge variant="secondary">Admin</Badge>{/if}
      </DropdownMenu.Label>
      <DropdownMenu.Separator />
      <DropdownMenu.Item onSelect={() => navigate("/")}><FolderIcon />Repositories</DropdownMenu.Item>
      <DropdownMenu.Item onSelect={() => navigate("/settings/profile")}><SettingsIcon />Settings</DropdownMenu.Item>
      <DropdownMenu.Separator />
      <DropdownMenu.Label class="text-xs font-normal text-muted-foreground">Theme</DropdownMenu.Label>
      <DropdownMenu.RadioGroup
        value={userPrefersMode.current}
        onValueChange={(v) => setMode(v as "light" | "dark" | "system")}
      >
        <DropdownMenu.RadioItem value="light">Light</DropdownMenu.RadioItem>
        <DropdownMenu.RadioItem value="dark">Dark</DropdownMenu.RadioItem>
        <DropdownMenu.RadioItem value="system">Same as the system</DropdownMenu.RadioItem>
      </DropdownMenu.RadioGroup>
      <DropdownMenu.Separator />
      <DropdownMenu.Item onSelect={() => auth.signOut()}><LogOutIcon />Sign out</DropdownMenu.Item>
    </DropdownMenu.Content>
  </DropdownMenu.Root>
{:else}
  <Button
    variant="ghost"
    size="icon"
    aria-label={mode.current === "dark" ? "Use the light theme" : "Use the dark theme"}
    onclick={() => setMode(mode.current === "dark" ? "light" : "dark")}
  >
    {#if mode.current === "dark"}<SunIcon />{:else}<MoonIcon />{/if}
  </Button>
  {#if router.path !== "/login"}
    <Button href={href("/login", { next })}>Sign in</Button>
  {/if}
{/if}
