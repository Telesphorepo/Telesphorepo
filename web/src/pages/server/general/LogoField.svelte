<!--
  The control of the server logo on Settings › General: a preview of the logo (or the brand's mark
  without one), Upload logo, and Remove. Each is an action of its own route that applies at once
  (PUT and DELETE /api/identity/logo), outside the form's draft: Upload is the person's choice of
  a file, Remove asks first. The answer updates the identity of the whole UI (brand).
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, errorMessage } from "$lib/api";
  import { brand, productName } from "$lib/brand.svelte";
  import { Button } from "$lib/components/ui/button";
  import Logo from "$lib/components/Logo.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import UploadIcon from "@lucide/svelte/icons/upload";

  let {
    id,
    disabled = false,
    describedby,
    onchange,
  }: {
    /** The id of the Upload button, for the label. */
    id: string;
    disabled?: boolean;
    describedby?: string;
    /** Called after the logo changed. */
    onchange: () => void;
  } = $props();

  let input = $state<HTMLInputElement>();
  let busy = $state(false);
  let removing = $state(false);

  async function upload(e: Event & { currentTarget: HTMLInputElement }) {
    const file = e.currentTarget.files?.[0];
    e.currentTarget.value = "";
    if (!file) return;
    busy = true;
    try {
      brand.set(await api.identity.putLogo(file));
      toast.success("Uploaded the logo");
      onchange();
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      busy = false;
    }
  }

  async function remove() {
    brand.set(await api.identity.deleteLogo());
    toast.success("Removed the logo");
    onchange();
  }
</script>

<div class="flex flex-wrap items-center gap-4">
  <div class="flex h-16 min-w-24 items-center justify-center rounded-lg border bg-panel px-4">
    {#if brand.logo}
      <img src={brand.logo} alt="The logo of this server" class="h-10 max-w-40 object-contain" />
    {:else}
      <Logo size={30} />
    {/if}
  </div>
  <div class="flex gap-2">
    <input
      bind:this={input}
      type="file"
      accept="image/png,image/jpeg,image/webp"
      class="sr-only"
      tabindex="-1"
      aria-hidden="true"
      onchange={upload}
    />
    <Button {id} type="button" variant="outline" disabled={disabled || busy} aria-describedby={describedby} onclick={() => input?.click()}>
      <UploadIcon />{brand.logo ? "Replace logo" : "Upload logo"}
    </Button>
    {#if brand.logo}
      <Button type="button" variant="ghost" class="hover:text-destructive" disabled={disabled || busy} onclick={() => (removing = true)}>Remove</Button>
    {/if}
  </div>
</div>

<ConfirmDialog
  bind:open={removing}
  title="Remove the logo?"
  changes={[`The top bar and the sign-in page show the ${productName} mark again`]}
  confirmLabel="Remove logo"
  destructive
  onConfirm={remove}
/>
