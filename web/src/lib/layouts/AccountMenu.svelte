<!-- The avatar menu of a signed-in user: your account, the CLI install page, the theme, sign out. -->
<script lang="ts">
  import * as DropdownMenu from "$lib/components/ui/dropdown-menu";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import { buttonVariants } from "$lib/components/ui/button";
  import { setMode, userPrefersMode } from "mode-watcher";
  import UserIcon from "@lucide/svelte/icons/user-round";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import LogOutIcon from "@lucide/svelte/icons/log-out";
  import { auth } from "$lib/auth.svelte";
  import { navigate } from "$lib/router.svelte";
  import { serverPhrase } from "$lib/permissions";
</script>

{#if auth.user}
  <DropdownMenu.Root>
    <DropdownMenu.Trigger class={buttonVariants({ variant: "ghost", size: "icon" })} aria-label="Account menu">
      <UserAvatar name={auth.user.username} size="md" />
    </DropdownMenu.Trigger>
    <DropdownMenu.Content align="end" class="w-60">
      <DropdownMenu.Label class="flex flex-col gap-1 font-normal">
        <span class="truncate font-medium text-foreground">{auth.user.username}</span>
        <span class="truncate text-xs text-muted-foreground">{serverPhrase(auth.user.permissions) || "Member"}</span>
      </DropdownMenu.Label>
      <DropdownMenu.Separator />
      <DropdownMenu.Item onSelect={() => navigate("/account")}><UserIcon />Your account</DropdownMenu.Item>
      <DropdownMenu.Item onSelect={() => navigate("/install")}><TerminalIcon />Install the CLI</DropdownMenu.Item>
      <DropdownMenu.Separator />
      <DropdownMenu.Label class="text-xs font-normal text-muted-foreground">Theme</DropdownMenu.Label>
      <DropdownMenu.RadioGroup value={userPrefersMode.current} onValueChange={(v) => setMode(v as "light" | "dark" | "system")}>
        <DropdownMenu.RadioItem value="light">Light</DropdownMenu.RadioItem>
        <DropdownMenu.RadioItem value="dark">Dark</DropdownMenu.RadioItem>
        <DropdownMenu.RadioItem value="system">Same as the system</DropdownMenu.RadioItem>
      </DropdownMenu.RadioGroup>
      <DropdownMenu.Separator />
      <DropdownMenu.Item onSelect={() => auth.signOut()}><LogOutIcon />Sign out</DropdownMenu.Item>
    </DropdownMenu.Content>
  </DropdownMenu.Root>
{/if}
