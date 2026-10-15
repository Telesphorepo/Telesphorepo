<!-- The fallback repo-config editor: the config as a JSON object. One FormRow of the page's FormGrid. -->
<script lang="ts">
  import { untrack } from "svelte";
  import { Textarea } from "$lib/components/ui/textarea";
  import FormRow from "$lib/components/FormRow.svelte";
  import type { EditorProps } from "./index";

  let { value, onChange }: EditorProps = $props();

  const id = $props.id();
  // The editor owns its text after the first render; wrap it in {#key} to start over.
  let text = $state(untrack(() => JSON.stringify(value, null, 2)));
  let error = $state("");

  function edit() {
    try {
      const v = JSON.parse(text);
      if (v === null || typeof v !== "object" || Array.isArray(v)) throw new Error("The options must be a JSON object, for example {}.");
      error = "";
      onChange(v);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      onChange(null);
    }
  }
</script>

<FormRow label="Format options" for="{id}-json" help="The options of this format, as a JSON object." error={error || undefined}>
  <Textarea
    id="{id}-json"
    class="min-h-28 font-mono text-[0.8125rem]"
    rows={Math.min(14, Math.max(4, text.split("\n").length))}
    spellcheck={false}
    aria-describedby="{id}-json-help"
    bind:value={text}
    oninput={edit}
    aria-invalid={error ? true : undefined}
  />
</FormRow>
