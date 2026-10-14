<!--
  The repo-config editor of a format: its custom editor from the registry, else the JSON editor.
  Wrap it in {#key format + name} so its state resets when the format or the repo changes.
-->
<script lang="ts">
  import { editors, type EditorProps } from "./index";
  import JsonEditor from "./JsonEditor.svelte";

  let { format, value, onChange }: EditorProps & { format: string } = $props();

  const Editor = $derived(editors[format] ?? JsonEditor);
</script>

<Editor {value} {onChange} />
