<!--
  Install the Telesphorepo CLI (/install?os=&cpu=). One tab per operating system, the browser's
  own first: the install line of /install.sh or /install.ps1, the archives of /cli/ with the
  SHA-256 of each (GET /cli/SHA256SUMS) and the commands that install the chosen archive by hand,
  and telesphorepo update. The page builds the lines from its own address, like the setup page.
  A server built without the CLI archives answers 404, and the page says so.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href } from "$lib/router.svelte";
  import { api, ApiError, type CLIArchive } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import TabNav from "$lib/components/TabNav.svelte";
  import Section from "$lib/components/Section.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CopyButton from "$lib/components/CopyButton.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import DownloadIcon from "@lucide/svelte/icons/download";
  import PackageIcon from "@lucide/svelte/icons/package";
  import TerminalIcon from "@lucide/svelte/icons/terminal";

  let { query }: PageProps = $props();

  type OS = "linux" | "darwin" | "windows";
  const systems: { id: OS; label: string }[] = [
    { id: "linux", label: "Linux" },
    { id: "darwin", label: "macOS" },
    { id: "windows", label: "Windows" },
  ];

  function browserOS(): OS {
    if (typeof navigator === "undefined") return "linux";
    const p = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform || navigator.platform || navigator.userAgent;
    if (/win/i.test(p)) return "windows";
    if (/mac|iphone|ipad/i.test(p)) return "darwin";
    return "linux";
  }

  const os = $derived<OS>(systems.some((s) => s.id === query.get("os")) ? (query.get("os") as OS) : browserOS());
  const windows = $derived(os === "windows");

  // Most Macs sold today have Apple silicon; most other computers an x86-64 CPU.
  const cpu = $derived(query.get("cpu") === "arm64" || query.get("cpu") === "amd64" ? query.get("cpu")! : os === "darwin" ? "arm64" : "amd64");

  const base = location.origin + location.pathname.replace(/\/+$/, "");
  const line = $derived(windows ? `irm ${base}/install.ps1 | iex` : `curl -fsSL ${base}/install.sh | sh`);

  const archives = new Resource(async () => {
    try {
      return await api.cli.archives();
    } catch (e) {
      // A server built without the CLI archives answers 404 (no-cli-downloads).
      if (e instanceof ApiError && e.notFound) return [];
      throw e;
    }
  });
  const none = $derived(archives.data !== undefined && archives.data.length === 0);
  const ofOS = (items: CLIArchive[]) => items.filter((a) => a.os === os);
  const chosen = $derived((archives.data ?? []).find((a) => a.os === os && a.arch === cpu));

  function cpuLabel(a: CLIArchive): string {
    if (a.os === "darwin") return a.arch === "arm64" ? "Apple silicon" : "Intel";
    return a.arch === "arm64" ? "ARM64" : "x86-64 (Intel, AMD)";
  }

  const commands = $derived.by(() => {
    const a = chosen;
    if (!a) return "";
    if (a.os === "windows") {
      return [
        `$f = '${a.name}'`,
        `Invoke-WebRequest -UseBasicParsing ${base}/cli/$f -OutFile $f`,
        `if ((Get-FileHash $f -Algorithm SHA256).Hash -ne '${a.sha256}') { throw "The SHA-256 of $f differs; do not install it" }`,
        `$dir = "$env:LOCALAPPDATA\\Programs\\telesphorepo"`,
        `Expand-Archive $f -DestinationPath $dir -Force`,
        `Copy-Item "$dir\\telesphorepo.exe" "$dir\\docker-credential-telesphorepo.exe" -Force`,
        `[Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ";$dir", 'User')`,
      ].join("\n");
    }
    const check = a.os === "darwin" ? "shasum -a 256 -c -" : "sha256sum -c -";
    return [
      `curl -fLO ${base}/cli/${a.name}`,
      `echo '${a.sha256}  ${a.name}' | ${check}`,
      `tar -xzf ${a.name} telesphorepo`,
      `mkdir -p ~/.local/bin`,
      `mv telesphorepo ~/.local/bin/`,
      `ln -sf telesphorepo ~/.local/bin/docker-credential-telesphorepo`,
    ].join("\n");
  });
</script>

<PageHeader title="Install the CLI" description="The telesphorepo command signs your computer in, sets up a project and publishes it from a terminal." />

<TabNav label="Operating system" items={systems.map((s) => ({ label: s.label, href: href("/install", { os: s.id }), active: s.id === os }))} />

{#if archives.loading && !archives.data}
  <Loading rows={6} />
{:else if archives.error && !archives.data}
  <ErrorState error={archives.error} title="The CLI downloads did not load" onRetry={archives.reload} />
{:else if none}
  <EmptyState icon={TerminalIcon} title="This server has no CLI downloads" description="It was built without the CLI archives. Ask the administrator to build it with go tool task build, or to install a release." />
{:else}
  <Section
    title="Install"
    description={windows
      ? "Run this line in PowerShell. The script checks the SHA-256 of the download and installs telesphorepo for your user."
      : "Run this line in a terminal. The script checks the SHA-256 of the download and installs telesphorepo into ~/.local/bin."}
  >
    <div class="flex flex-col gap-3">
      <CodeSnippet label={windows ? "PowerShell" : "Terminal"} value={line} />
      <p class="max-w-prose text-meta text-pretty text-muted-foreground">
        {#if windows}
          The script adds its folder to your PATH. This PowerShell window can run telesphorepo at once; open a new terminal to run it anywhere else.
        {:else}
          When ~/.local/bin is not on your PATH, the script offers to add it to the start-up file of your shell. Open a new terminal afterwards.
        {/if}
        To set up a project, open the Publish tab of a repository.
      </p>
    </div>
  </Section>

  <Section title="Install by hand" description="Pick the archive for your computer, check its SHA-256 and unpack it, if you would rather not run a script.">
    <div class="flex flex-col gap-6">
      <ItemList
        source={listOf(archives, ofOS)}
        caption="CLI archives"
        key={(a) => a.name}
        href={(a) => href("/install", { os, cpu: a.arch })}
        selected={(a) => a.arch === cpu}
      >
        {#snippet leading()}<PackageIcon class="size-5" />{/snippet}
        {#snippet title(a)}{a.name}{/snippet}
        {#snippet meta(a)}
          <span class="flex min-w-0 items-center gap-1">
            <span class="shrink-0">{cpuLabel(a)} ·</span>
            <span class="truncate tabular-nums" title={a.sha256}>SHA-256 {a.sha256.slice(0, 16)}…</span>
            <CopyButton value={a.sha256} label="Copy the SHA-256 of {a.name}" size="icon-xs" />
          </span>
        {/snippet}
        {#snippet trailing(a)}
          <Button variant="ghost" size="sm" href={a.url} download={a.name}><DownloadIcon />Download</Button>
        {/snippet}
        {#snippet empty()}
          <CompactEmpty icon={PackageIcon} title="Archives" state="None for this system" text="This server was built without a CLI for this operating system." />
        {/snippet}
      </ItemList>
      {#if chosen}
        <div class="flex flex-col gap-3">
          <CodeBlock title={windows ? `PowerShell, for ${chosen.name}` : `Terminal, for ${chosen.name}`} code={commands} />
          <p class="max-w-prose text-meta text-pretty text-muted-foreground">
            {#if windows}
              The last line adds the folder to your PATH; open a new terminal afterwards.
            {:else}
              Then put ~/.local/bin on your PATH, if it is not there yet, and open a new terminal.
            {/if}
          </p>
        </div>
      {/if}
    </div>
  </Section>

  <Section title="Update" description="After the server is updated, run this to install the CLI of the new server version.">
    <div class="flex flex-col gap-3">
      <CodeSnippet value="telesphorepo update" />
      <p class="max-w-prose text-meta text-pretty text-muted-foreground">On a computer that uses more than one server, add the server address after update.</p>
    </div>
  </Section>
{/if}
