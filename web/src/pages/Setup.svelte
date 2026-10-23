<!--
  A setup link (/#/setup/<code>) from the Publish tab of a repository. The page tells the person to
  run the install line in a terminal in the project folder. The code is in the address fragment,
  so the browser never sends it to the server, and this page makes no API call: it builds the line
  from the address of the page.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import Step from "$lib/components/Step.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import Segmented from "$lib/components/Segmented.svelte";

  let { params }: PageProps = $props();

  let os = $state<"unix" | "windows">(typeof navigator !== "undefined" && /Windows/i.test(navigator.userAgent) ? "windows" : "unix");

  const base = location.origin + location.pathname.replace(/\/+$/, "");
  const link = $derived(`${base}/#/setup/${params.code}`);
  const line = $derived(
    os === "windows"
      ? `& ([scriptblock]::Create((irm ${base}/install.ps1))) setup '${link}'`
      : `curl -fsSL ${base}/install.sh | sh -s -- setup '${link}'`,
  );
</script>

<PageHeader title="Set up a project" description="Run one line in a terminal. It signs this computer in and sets up the project." />

<FormPage>
  <Step n={1} title="Run this line in a terminal in the project folder" description="It works once and ends 10 minutes after it was made. The code stays in your browser: the server never receives it." last>
    <div class="flex flex-col gap-4">
      <Segmented
        bind:value={os}
        label="Operating system"
        options={[
          { value: "unix", label: "macOS or Linux" },
          { value: "windows", label: "Windows" },
        ]}
      />
      <CodeSnippet label={os === "windows" ? "PowerShell" : "Terminal"} value={line} />
    </div>
  </Step>
</FormPage>