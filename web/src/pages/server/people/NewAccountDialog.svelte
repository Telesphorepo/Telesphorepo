<!--
  New account (People): a short form with the name and the password of a local account. After
  Create, the new person's page opens to set the permissions. People and the Settings overview
  open it.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { navigate } from "$lib/router.svelte";
  import { api } from "$lib/api";
  import { Input } from "$lib/components/ui/input";
  import FormField from "$lib/components/FormField.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";

  let { open = $bindable(false) }: { open?: boolean } = $props();

  const id = $props.id();
  let name = $state("");
  let password = $state("");

  // Each opening starts with an empty form.
  $effect(() => {
    if (open) name = password = "";
  });

  async function create() {
    const u = await api.users.create({ username: name.trim(), password, permissions: [] });
    toast.success(`Created the account ${u.username}`);
    navigate(`/settings/people/${encodeURIComponent(u.username)}`);
  }
</script>

<ShortFormDialog
  bind:open
  title="New account"
  description="The person signs in with this name and password. Set the permissions on the next page."
  submitLabel="Create account"
  canSubmit={!!name.trim() && !!password}
  onSubmit={create}
>
  <FormField label="Username" for="{id}-name">
    <Input id="{id}-name" autocomplete="off" spellcheck={false} bind:value={name} />
  </FormField>
  <FormField label="Password" for="{id}-pw">
    <Input id="{id}-pw" type="password" autocomplete="new-password" bind:value={password} />
  </FormField>
</ShortFormDialog>
