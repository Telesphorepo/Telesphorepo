<!--
  The question before leaving a view with unsaved changes (lib/unsaved.svelte.ts). The shell
  renders it once. Cancel keeps the user on the page with the edits; the confirm discards them and
  opens the address the user chose.
-->
<script lang="ts">
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import { unsaved } from "$lib/unsaved.svelte";

  unsaved.install();

  // The count when the question opened; it stays while the dialog closes after a discard.
  let n = $state(0);
  $effect(() => {
    if (unsaved.pending) n = unsaved.count;
  });
</script>

<ConfirmDialog
  bind:open={() => unsaved.pending !== null, (v) => { if (!v) unsaved.stay(); }}
  title="Leave this page?"
  destructive
  confirmLabel="Discard and leave"
  changes={[n === 1 ? "Discards 1 unsaved change on this page" : `Discards ${n} unsaved changes on this page`]}
  onConfirm={() => unsaved.leave()}
/>
