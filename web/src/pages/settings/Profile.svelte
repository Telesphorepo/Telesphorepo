<!-- Account: who the user is, and the password change for a local account. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api } from "$lib/api";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";

  let { params, query }: PageProps = $props();

  const user = $derived(auth.user);
  const local = $derived(user?.kind === "local");

  let password = $state("");
  let repeat = $state("");
  let formError = $state("");
  let open = $state(false);

  function submit(e: SubmitEvent) {
    e.preventDefault();
    formError = "";
    if (!password) {
      formError = "Enter a new password.";
    } else if (password !== repeat) {
      formError = "The two passwords differ. Enter the same password twice.";
    } else {
      open = true;
    }
  }

  async function change() {
    if (!user) return;
    await api.users.update(user.username, { password });
    password = "";
    repeat = "";
    toast.success("Changed the password");
  }
</script>

<PageHeader title="Profile and password" description="Your account and the password you sign in with." />

{#if user}
  <Section title="Account" narrow>
    <dl class="grid grid-cols-[8rem_1fr] gap-x-4 gap-y-3 text-sm">
      <dt class="text-muted-foreground">Username</dt>
      <dd class="font-mono break-all">{user.username}</dd>
      <dt class="text-muted-foreground">Role</dt>
      <dd>
        {#if user.is_admin}<Badge>Admin</Badge>{:else}<Badge variant="outline">User</Badge>{/if}
      </dd>
      <dt class="text-muted-foreground">Account type</dt>
      <dd>{local ? "Local" : "SSO"}</dd>
      {#if user.email}
        <dt class="text-muted-foreground">Email</dt>
        <dd class="break-all">{user.email}</dd>
      {/if}
      <dt class="text-muted-foreground">Created</dt>
      <dd><RelativeTime at={user.created_at} /></dd>
    </dl>
  </Section>

  <Section title="Password" narrow>
    {#if local}
      <form class="flex flex-col gap-4" onsubmit={submit}>
        <div class="flex flex-col gap-2">
          <Label for="new-password">New password</Label>
          <Input id="new-password" type="password" autocomplete="new-password" class="max-w-sm" bind:value={password} />
        </div>
        <div class="flex flex-col gap-2">
          <Label for="repeat-password">New password again</Label>
          <Input id="repeat-password" type="password" autocomplete="new-password" class="max-w-sm" bind:value={repeat} />
        </div>
        {#if formError}<ErrorState inline error={formError} />{/if}
        <div class="flex gap-2">
          <Button type="submit" disabled={!password || !repeat}>Change password</Button>
        </div>
      </form>
    {:else}
      <p class="text-sm text-muted-foreground">
        Your identity provider manages the password of a single sign-on account. Change it there.
      </p>
    {/if}
  </Section>

  <ConfirmDialog
    bind:open
    title="Change password"
    confirmLabel="Change password"
    changes={["Replaces your current password", "You sign in with the new password from now on"]}
    onConfirm={change}
  />
{/if}
