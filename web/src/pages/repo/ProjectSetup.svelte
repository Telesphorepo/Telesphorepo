<!--
  Set up a Gradle or Maven project with one command (GET /setup.sh or /setup.ps1 of the server).
  The person picks where they publish from and their OS, and copies the command with the
  repository and the server filled in. The command edits the build files in the project folder.
  The manual snippets of the format stay behind one "Set it up by hand" disclosure.
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import type { Snippet } from "./browse";
  import { href, repoPath } from "$lib/router.svelte";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import { Button } from "$lib/components/ui/button";
  import { Separator } from "$lib/components/ui/separator";
  import Section from "$lib/components/Section.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import LearnMore from "$lib/components/LearnMore.svelte";
  import Segmented from "./snippets/Segmented.svelte";
  import SnippetTabs from "./snippets/SnippetTabs.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { repo, baseURL, computer, ci }: { repo: Repo; baseURL: string; computer: Snippet[]; ci: Snippet[] } = $props();

  type Where = "computer" | "github" | "jenkins-pipeline" | "jenkins-freestyle";
  const places: { value: Where; label: string; description: string }[] = [
    { value: "computer", label: "Your computer only", description: "Each person publishes from their own computer and approves it once in the browser." },
    { value: "github", label: "GitHub Actions", description: "Also adds a workflow that publishes when you push a tag that starts with v." },
    { value: "jenkins-pipeline", label: "Jenkins with a Jenkinsfile", description: "Also adds a Jenkinsfile with a publish stage." },
    { value: "jenkins-freestyle", label: "Jenkins freestyle job", description: "Also prints the steps to add to the job in Jenkins." },
  ];
  let where = $state<Where>("computer");
  let os = $state<"unix" | "windows">(typeof navigator !== "undefined" && /Windows/i.test(navigator.userAgent) ? "windows" : "unix");
  let byHand = $state(false);

  const base = $derived(baseURL.replace(/\/+$/, ""));
  const command = $derived(
    os === "windows"
      ? `& ([scriptblock]::Create((irm ${base}/setup.ps1))) ${repo.name} -Ci ${where === "computer" ? "none" : where}`
      : `curl -fsSL ${base}/setup.sh | sh -s -- ${repo.name} --ci ${where === "computer" ? "none" : where}`,
  );
  const handCI = $derived(ci.filter((s) => (where === "github" ? s.client === "github-actions" : where !== "computer" && s.client === "jenkins")));
  const uid = $props.id();
</script>

{#snippet part(n: number, title: string, text?: string)}
  <div class="flex min-w-0 items-start gap-3">
    <span class="flex h-6 min-w-4 shrink-0 items-center text-label text-muted-foreground tabular-nums">{n}</span>
    <div class="min-w-0">
      <h3 class="text-label leading-6">{title}</h3>
      {#if text}<p class="max-w-prose text-meta text-muted-foreground">{text}</p>{/if}
    </div>
  </div>
{/snippet}

<Section title="Set up your project" description="Run one command in the project folder. It adds Telesphorepo to the build files for you.">
  <div class="flex flex-col gap-4">
    {@render part(1, "Where will you publish from?")}
    <RadioGroup.Root class="gap-1" aria-label="Where will you publish from?" bind:value={where}>
      {#each places as p (p.value)}
        <OptionRow id="{uid}-{p.value}" label={p.label} description={p.description}>
          {#snippet control()}<RadioGroup.Item id="{uid}-{p.value}" value={p.value} />{/snippet}
        </OptionRow>
      {/each}
    </RadioGroup.Root>
  </div>
  <Separator class="my-6" />
  <div class="flex flex-col gap-4">
    {@render part(2, "Run this in the project folder", "Copy the command to a terminal in the folder with settings.gradle.kts, settings.gradle or pom.xml. Then commit the files it changes.")}
    <Segmented
      bind:value={os}
      label="Operating system"
      options={[
        { value: "unix", label: "macOS or Linux" },
        { value: "windows", label: "Windows" },
      ]}
    />
    <CodeSnippet label={os === "windows" ? "PowerShell" : "Terminal"} value={command} />
  </div>
  {#if where !== "computer"}
    <Separator class="my-6" />
    <div class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
      {@render part(3, "Add a CI pipeline", `It names the CI repository and branches that may publish to ${repo.name}.`)}
      {#if repo.permissions?.includes("pipelines")}
        <Button variant="outline" class="shrink-0" href={href("/settings/ci-pipelines/new", { repo: repo.name, back: repoPath(repo.name, "publish") })}><PlusIcon />New CI pipeline</Button>
      {:else}
        <p class="text-meta text-muted-foreground sm:max-w-64 sm:text-right">Ask a person who manages the CI pipelines of this repository to add one for your CI repository and branch.</p>
      {/if}
    </div>
  {/if}
  <Separator class="my-6" />
  <div class="flex flex-col gap-4">
    {@render part(
      where === "computer" ? 3 : 4,
      "Publish",
      "Run ./gradlew publish or mvn deploy. The first time, the build opens your browser: click Approve. Each person approves their computer once.",
    )}
    <div class="flex flex-col">
      <LearnMore bind:open={byHand} label="Set it up by hand" toggleOnly controls="{uid}-hand" />
      {#if byHand}
        <div id="{uid}-hand" class="mt-4 flex flex-col gap-6">
          <SnippetTabs snippets={computer} label="Build tool" />
          {#if handCI.length}<SnippetTabs snippets={handCI} label="CI system" />{/if}
        </div>
      {/if}
    </div>
  </div>
</Section>
