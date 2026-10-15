<!-- Debian repo config: the architectures the indexes list. One FormRow of the page's FormGrid. -->
<script lang="ts">
  import { untrack } from "svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import ListInput from "$lib/components/ListInput.svelte";
  import type { EditorProps } from "./index";

  let { value, onChange }: EditorProps = $props();

  const id = $props.id();
  let architectures = $state(untrack(() => (Array.isArray(value.architectures) ? [...(value.architectures as string[])] : [])));
</script>

<FormRow label="Architectures" for="{id}-arch" help="The processor types that apt sees in this repository.">
  <ListInput
    id="{id}-arch"
    values={architectures}
    placeholder="Add an architecture, e.g. arm64"
    normalize={(s) => s.trim().toLowerCase()}
    describedby="{id}-arch-help"
    label="architecture"
    onchange={(v) => {
      architectures = v;
      onChange({ ...value, architectures: v });
    }}
  />
</FormRow>
