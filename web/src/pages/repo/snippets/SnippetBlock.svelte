<!--
  One snippet: its CodeBlock and its note. The title names the file and the tools it is for:
  ".github/workflows/publish.yml (Gradle and Maven)". A CI file with a `diff` (a GitHub Actions
  workflow, a Jenkinsfile) opens on the change to the reader's own file, "Add to a workflow", with
  "New workflow" (the whole file) one click away. The choice is remembered in this browser.
  `workflow` names the workflow file (a CI pipeline's), in place of publish.yml.

    <SnippetBlock snippet={s} label="Gradle and Maven" />
-->
<script lang="ts" module>
  /** "Gradle", "Gradle and Maven", "npm, Yarn and pnpm". */
  export const andList = (xs: string[]) => (xs.length <= 1 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);

  /** The snippets of one list, those with the same file, code, change and note as one entry with every title. */
  export function mergeSame<S extends { file?: string; code: string; diff?: string; note?: string; title: string }>(xs: S[]): { snippet: S; titles: string[] }[] {
    const out: { snippet: S; titles: string[]; key: string }[] = [];
    for (const s of xs) {
      const key = JSON.stringify([s.file ?? "", s.code, s.diff ?? "", s.note ?? ""]);
      const same = out.find((o) => o.key === key);
      if (same) {
        if (!same.titles.includes(s.title)) same.titles.push(s.title);
      } else out.push({ snippet: s, titles: [s.title], key });
    }
    return out.map(({ snippet, titles }) => ({ snippet, titles }));
  }

  const VIEW_KEY = "telesphorepo.ci.view";
  type View = "change" | "file";
  function rememberedView(): View {
    try {
      return localStorage.getItem(VIEW_KEY) === "file" ? "file" : "change";
    } catch {
      return "change";
    }
  }
</script>

<script lang="ts">
  import type { Snippet } from "$lib/api";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
  import Segmented from "$lib/components/Segmented.svelte";

  let { snippet, label, workflow = "" }: { snippet: Snippet; label?: string; workflow?: string } = $props();

  const WORKFLOWS = ".github/workflows/";

  const tools = $derived(label ?? snippet.title);
  const isWorkflow = $derived(!!snippet.file?.startsWith(WORKFLOWS));
  const file = $derived(isWorkflow && workflow ? WORKFLOWS + workflow : snippet.file);
  const fileTitle = $derived(file && !tools.includes(file) ? (tools ? `${file} (${tools})` : file) : tools);
  // The reader's own workflow has a name this page does not know, unless the pipeline names it.
  const changeTitle = $derived(isWorkflow && !workflow ? (tools ? `Your workflow (${tools})` : "Your workflow") : fileTitle);
  const noun = $derived(isWorkflow ? "workflow" : (snippet.file ?? "file"));

  let view = $state<View>(rememberedView());
  function choose(v: View) {
    view = v;
    try {
      localStorage.setItem(VIEW_KEY, v);
    } catch {
      /* storage blocked: the choice lasts for this page */
    }
  }
  const showChange = $derived(!!snippet.diff && view === "change");
</script>

{#snippet toggle()}
  <Segmented
    compact
    label="Show the change or the whole file"
    bind:value={() => view, choose}
    options={[
      { value: "change", label: `Add to a ${noun}` },
      { value: "file", label: `New ${noun}` },
    ]}
  />
{/snippet}

<div class="flex min-w-0 flex-col gap-2">
  <CodeBlock
    title={showChange ? changeTitle : fileTitle}
    code={snippet.code}
    diff={showChange ? snippet.diff : undefined}
    actions={snippet.diff ? toggle : undefined}
  />
  {#if snippet.note}<p class="text-meta text-muted-foreground">{snippet.note}</p>{/if}
</div>