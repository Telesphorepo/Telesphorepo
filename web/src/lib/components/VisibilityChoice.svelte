<!--
  Who can download from a repository: Private or Public (the API's anonymous_read).

    <VisibilityChoice bind:anonymous />
-->
<script lang="ts">
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import OptionRow from "./OptionRow.svelte";

  let { anonymous = $bindable(false) }: { anonymous?: boolean } = $props();

  const id = $props.id();
</script>

<RadioGroup.Root
  class="gap-1"
  aria-label="Who can download"
  value={anonymous ? "public" : "private"}
  onValueChange={(v) => (anonymous = v === "public")}
>
  <OptionRow id="{id}-private" label="Private" description="Only the people you give access can see and download the files.">
    {#snippet control()}<RadioGroup.Item id="{id}-private" value="private" />{/snippet}
  </OptionRow>
  <OptionRow
    id="{id}-public"
    label="Public"
    description="Anyone can see and download the files without signing in. Publishing still needs access."
  >
    {#snippet control()}<RadioGroup.Item id="{id}-public" value="public" />{/snippet}
  </OptionRow>
</RadioGroup.Root>
