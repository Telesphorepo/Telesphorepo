<!-- Account: a manual publish session for one repository, client setup snippets, and the caller's sessions. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type PublishSession, type Proof, type Repo, type SessionInfo } from "$lib/api";
  import { Keyset, Resource } from "$lib/load.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import CopyField from "$lib/components/CopyField.svelte";
  import FormatBadge from "$lib/components/FormatBadge.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import SecondFactorPrompt from "$lib/components/SecondFactorPrompt.svelte";
  import * as Select from "$lib/components/ui/select";
  import * as Table from "$lib/components/ui/table";
  import * as Tabs from "$lib/components/ui/tabs";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Label } from "$lib/components/ui/label";
  import PackageIcon from "@lucide/svelte/icons/package";

  let { params, query }: PageProps = $props();

  const HOST = window.location.origin;
  const USER = "publish";

  const repos = new Resource(() => api.repos.list());
  const sessions = new Keyset((p) => api.publish.sessions(p));

  const writable = $derived(
    (repos.data ?? []).filter((r) => auth.isAdmin || r.permission === "write" || r.permission === "admin"),
  );

  let chosen = $state("");
  const repo = $derived(writable.find((r) => r.name === chosen) ?? writable[0]);

  let promptOpen = $state(false);
  let session = $state<PublishSession | null>(null);
  let endOpen = $state(false);

  async function create(proof: Proof) {
    if (!repo) return;
    session = await api.publish.manualSession(repo.name, proof);
    toast.success(`Created a publish session for ${session.repo}`);
    await sessions.reload();
  }

  async function end() {
    if (!session) return;
    await api.publish.closeSession(session.id, session.token);
    toast.success(`Ended the publish session for ${session.repo}`);
    session = null;
    await sessions.reload();
  }

  const repoUrl = (r: Repo) => `${HOST}/${r.format}/${r.name}`;

  function sessionSnippets(r: Repo, token: string): { title: string; lang: string; code: string }[] {
    const url = repoUrl(r);
    const out: { title: string; lang: string; code: string }[] = [];
    if (r.format === "maven") {
      out.push(
        {
          title: "Maven: settings.xml",
          lang: "xml",
          code: `<settings>
  <servers>
    <server>
      <id>telesphorepo</id>
      <username>${USER}</username>
      <password>${token}</password>
    </server>
  </servers>
</settings>
<!-- pom.xml: <distributionManagement><repository><id>telesphorepo</id><url>${url}</url></repository></distributionManagement> -->`,
        },
        {
          title: "Gradle: publishing block",
          lang: "kotlin",
          code: `publishing {
    repositories {
        maven {
            url = uri("${url}")
            credentials {
                username = "${USER}"
                password = "${token}"
            }
        }
    }
}`,
        },
      );
    }
    const path = r.format === "debian" ? `${url}/upload/DIST/COMPONENT` : `${url}/PATH/FILE`;
    out.push({ title: "curl", lang: "sh", code: `curl -f -u ${USER}:${token} -T FILE ${path}` });
    return out;
  }

  const sessionRepo = $derived(session ? (repos.data ?? []).find((r) => r.name === session!.repo) : undefined);

  const gradleSnippet = `// settings.gradle.kts
plugins { id("com.telesphorepo") version "VERSION" }
telesphorepo { url = "${HOST}" }

// Once per machine. It prints a code. Approve it at ${HOST}/#/device.
./gradlew telesphorepoLogin

// In CI the plugin uses the CI identity token instead (see "CI").`;

  const mavenSnippet = `<!-- .mvn/extensions.xml -->
<extensions>
  <extension>
    <groupId>com.telesphorepo</groupId>
    <artifactId>telesphorepo-maven-extension</artifactId>
    <version>VERSION</version>
  </extension>
</extensions>

# .mvn/maven.config
-Dtelesphorepo.url=${HOST}

# Once per machine. It prints a code. Approve it at ${HOST}/#/device.
mvn com.telesphorepo:telesphorepo-maven-plugin:VERSION:login -Dtelesphorepo.url=${HOST}

# settings.xml needs no password for this server.`;

  const ciRepo = $derived(writable[0]?.name ?? "REPO");
  const exchange = (oidc: string) => `SESSION=$(curl -fsS -X POST ${HOST}/api/publish/sessions/oidc \\
  -H 'Content-Type: application/json' \\
  -d "{\\"token\\":\\"${oidc}\\",\\"repo\\":\\"${ciRepo}\\"}")
TOKEN=$(echo "$SESSION" | jq -r .token)
ID=$(echo "$SESSION" | jq -r .id)`;
  const closeCmd = $derived(`curl -fsS -X DELETE -u ${USER}:$TOKEN ${HOST}/api/publish/sessions/$ID`);

  const githubSnippet = $derived(`# the job needs: permissions: { id-token: write }
OIDC=$(curl -fsS -H "Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \\
  "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=${HOST}" | jq -r .value)
${exchange("$OIDC")}
curl -f -u ${USER}:$TOKEN -T FILE ${HOST}/raw/${ciRepo}/PATH/FILE
${closeCmd}`);

  const jenkinsSnippet = $derived(`// Jenkins with the OpenID Connect Provider plugin: bind an "OpenID Connect id token"
// credential (audience ${HOST}) to the variable OIDC_TOKEN.
sh '''
${exchange("$OIDC_TOKEN")}
curl -f -u ${USER}:$TOKEN -T FILE ${HOST}/raw/${ciRepo}/PATH/FILE
${closeCmd}
'''`);

  const stateVariant = (s: SessionInfo) => (s.state === "active" ? "default" : "secondary");
</script>

<PageHeader
  title="New publish session"
  description="Writes need a publish session. A password or a read token can only read. A session works for one repository and ends after a short time."
/>

<Section title="Create a session" narrow>
  {#if session}
    <div class="flex flex-col gap-4">
      <p class="text-sm">
        Publish session for <span class="font-medium">{session.repo}</span>. It ends <RelativeTime at={session.expires_at} />.
        Use the token as the password with any username, for example <code class="font-mono">{USER}</code>.
      </p>
      <CopyField label="Session token" value={session.token} secret />
      {#if sessionRepo}
        {#each sessionSnippets(sessionRepo, session.token) as s (s.title)}
          <CopyField label={s.title} value={s.code} block />
        {/each}
      {/if}
      <div class="flex gap-2">
        <Button variant="destructive" onclick={() => (endOpen = true)}>End session now</Button>
        <Button variant="outline" onclick={() => (session = null)}>Hide the token</Button>
      </div>
    </div>
  {:else if repos.data}
    {#if writable.length === 0}
      <EmptyState
        icon={PackageIcon}
        title="No repository with write access"
        description="Ask an administrator to give you write access to a repository."
      />
    {:else}
      <div class="flex flex-col gap-4">
        <div class="flex flex-col gap-2">
          <Label for="session-repo">Repository</Label>
          <Select.Root type="single" bind:value={chosen}>
            <Select.Trigger id="session-repo" class="w-full max-w-sm">
              <span class="flex min-w-0 items-center gap-2">
                <span class="truncate font-mono">{repo?.name}</span>
                {#if repo}<FormatBadge format={repo.format} />{/if}
              </span>
            </Select.Trigger>
            <Select.Content>
              {#each writable as r (r.name)}
                <Select.Item value={r.name} label={r.name}>
                  {r.name}
                  <FormatBadge format={r.format} />
                </Select.Item>
              {/each}
            </Select.Content>
          </Select.Root>
        </div>
        {#if repo?.locked_by}
          <p class="text-sm text-muted-foreground">Uploads to this repository are paused: {repo.locked_by}.</p>
        {/if}
        <p class="text-sm text-muted-foreground">
          Confirm with your second factor. The token works for this repository only.
        </p>
        <div class="flex gap-2">
          <Button disabled={!repo} onclick={() => (promptOpen = true)}>Create session</Button>
        </div>
      </div>
    {/if}
  {:else if repos.error}
    <ErrorState error={repos.error} onRetry={repos.reload} />
  {:else}
    <LoadingRows rows={3} />
  {/if}
</Section>

<Section title="Your sessions">
  <KeysetTable list={sessions} columns={5} caption="Your publish sessions">
    {#snippet head()}
      <Table.Head>Repository</Table.Head>
      <Table.Head>Created by</Table.Head>
      <Table.Head>Created</Table.Head>
      <Table.Head>Ends</Table.Head>
      <Table.Head>State</Table.Head>
    {/snippet}
    {#snippet row(s)}
      <Table.Row>
        <Table.Cell class="font-medium">{s.repo}</Table.Cell>
        <Table.Cell>{s.via === "device" ? (s.device_name ?? "device") : s.via === "oidc" ? (s.trust_name ?? "CI") : "manual"}</Table.Cell>
        <Table.Cell><RelativeTime at={s.created_at} /></Table.Cell>
        <Table.Cell><RelativeTime at={s.closed_at ?? s.expires_at} /></Table.Cell>
        <Table.Cell><Badge variant={stateVariant(s)}>{s.state}</Badge></Table.Cell>
      </Table.Row>
    {/snippet}
    {#snippet empty()}
      <EmptyState title="No publish sessions yet" description="A session appears here when you or a device create one." />
    {/snippet}
  </KeysetTable>
</Section>

<Section
  title="Gradle and Maven clients"
  description="The Gradle plugin and the Maven extension create a publish session for every publish and end it afterwards. Use the latest release in place of VERSION."
>
  <Tabs.Root value="gradle">
    <Tabs.List>
      <Tabs.Trigger value="gradle">Gradle plugin</Tabs.Trigger>
      <Tabs.Trigger value="maven">Maven extension</Tabs.Trigger>
    </Tabs.List>
    <Tabs.Content value="gradle" class="mt-4"><CopyField value={gradleSnippet} block /></Tabs.Content>
    <Tabs.Content value="maven" class="mt-4"><CopyField value={mavenSnippet} block /></Tabs.Content>
  </Tabs.Root>
  <p class="mt-3 text-sm text-muted-foreground">
    The source is at
    <a class="text-link underline-offset-4 hover:underline" href="https://github.com/Telesphorepo/clients" target="_blank" rel="noreferrer">github.com/Telesphorepo/clients</a>.
  </p>
</Section>

<Section
  title="CI"
  description={"A CI job proves its identity with the signed token of the CI system. An administrator must add a trust policy first. The exchange answers {id, token, repo, expires_at}. Use the token as the password, then end the session."}
>
  <Tabs.Root value="github">
    <Tabs.List>
      <Tabs.Trigger value="github">GitHub Actions</Tabs.Trigger>
      <Tabs.Trigger value="jenkins">Jenkins</Tabs.Trigger>
    </Tabs.List>
    <Tabs.Content value="github" class="mt-4"><CopyField value={githubSnippet} block /></Tabs.Content>
    <Tabs.Content value="jenkins" class="mt-4"><CopyField value={jenkinsSnippet} block /></Tabs.Content>
  </Tabs.Root>
</Section>

<SecondFactorPrompt
  bind:open={promptOpen}
  title="Create a publish session"
  actionLabel="Create session"
  onProof={create}
/>

<ConfirmDialog
  bind:open={endOpen}
  title="End publish session"
  confirmLabel="End session"
  destructive
  changes={[`Closes the session for ${session?.repo ?? ""} at once`, "The token stops working"]}
  onConfirm={end}
/>
