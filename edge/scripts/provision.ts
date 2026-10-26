// bun run provision [wrangler.jsonc]
//
// Creates what the edge Worker needs in the Cloudflare account, from the wrangler.jsonc that the
// server generates (in the Worker folder of Settings > Edge delivery, or GET
// /api/edge/wrangler.jsonc for a checkout of the repository):
//
//   - each regional bucket (binding R2_<HINT>) with its location hint, when it does not exist
//   - the copy queue, when it does not exist
//   - the event notification rule of the home bucket: object-create and object-delete under
//     [s3.prefix/]blobs/, sent to the copy queue
//
// It runs again safely: it skips what exists. Run it after a region change, then `bun run deploy`.
//
// It runs wrangler as you are signed in: sign in once with `bunx wrangler login`. In CI, set
// CLOUDFLARE_API_TOKEN (Workers Admin, Workers R2 Storage Edit, Queues Edit, and Workers Routes
// Edit and Zone Read on the zones of the routes) instead. It asks
// nothing, so when your login reaches several accounts it needs CLOUDFLARE_ACCOUNT_ID (or
// "account_id" in wrangler.jsonc).
//
// It refuses a config whose routes are not on the zone of their host, because a cross-zone
// fetch from the Worker would reach the origin without the client address. The zone of every
// route must be in the Cloudflare account of the Worker; wrangler deploy refuses one that is not.

export {};

const HINTS = ["wnam", "enam", "weur", "eeur", "apac", "oc"];

function fail(msg: string): never {
  console.error("provision: " + msg);
  process.exit(1);
  throw new Error(msg);
}

// stripJSONC removes comments and trailing commas outside strings.
function stripJSONC(src: string): string {
  let out = "";
  let i = 0;
  while (i < src.length) {
    const c = src[i];
    if (c === '"') {
      let j = i + 1;
      while (j < src.length && src[j] !== '"') j += src[j] === "\\" ? 2 : 1;
      out += src.slice(i, j + 1);
      i = j + 1;
    } else if (c === "/" && src[i + 1] === "/") {
      while (i < src.length && src[i] !== "\n") i++;
    } else if (c === "/" && src[i + 1] === "*") {
      const end = src.indexOf("*/", i + 2);
      i = end < 0 ? src.length : end + 2;
    } else {
      out += c;
      i++;
    }
  }
  return out.replace(/,(\s*[}\]])/g, "$1");
}

const decoder = new TextDecoder();

// wrangler runs a wrangler command of the pinned version. quiet captures its output: stdout is
// the standard output alone, out both streams.
function wrangler(args: string[], quiet: boolean): { ok: boolean; stdout: string; out: string } {
  const r = Bun.spawnSync([process.execPath, "x", "wrangler", ...args], {
    stdout: quiet ? "pipe" : "inherit",
    stderr: quiet ? "pipe" : "inherit",
    stdin: "ignore",
  });
  const stdout = r.stdout ? decoder.decode(r.stdout) : "";
  return { ok: r.exitCode === 0, stdout, out: stdout + (r.stderr ? decoder.decode(r.stderr) : "") };
}

interface WhoAmI {
  loggedIn?: unknown;
  accounts?: { id?: unknown; name?: unknown }[];
}

// checkLogin fails unless wrangler is signed in (wrangler login, or CLOUDFLARE_API_TOKEN), and
// unless the account is clear: wrangler asks which account when a login reaches several, and it
// cannot ask here, since provision runs it without a terminal.
function checkLogin(accountID: string): void {
  const r = wrangler(["whoami", "--json"], true);
  const start = r.stdout.indexOf("{");
  const end = r.stdout.lastIndexOf("}");
  let who: WhoAmI | undefined;
  try {
    who = start >= 0 && end > start ? (JSON.parse(r.stdout.slice(start, end + 1)) as WhoAmI) : undefined;
  } catch {
    who = undefined;
  }
  if (!who) fail(`wrangler whoami failed:\n${r.out}`);
  if (who.loggedIn !== true) fail("Sign in first: bunx wrangler login");
  const accounts = (who.accounts ?? []).filter((a) => typeof a.id === "string") as { id: string; name?: unknown }[];
  const list = accounts.map((a) => `  ${typeof a.name === "string" ? a.name : "(no name)"}: ${a.id}`).join("\n");
  if (accountID !== "") {
    if (accounts.length > 0 && !accounts.some((a) => a.id === accountID)) {
      fail(`your Cloudflare login cannot reach the account ${accountID}. Your accounts are:\n${list}`);
    }
    return;
  }
  if (accounts.length > 1) {
    fail(
      `your Cloudflare login reaches ${accounts.length} accounts, and provision cannot ask which one. ` +
        `Set CLOUDFLARE_ACCOUNT_ID to the ID of the account of the R2 buckets (for example CLOUDFLARE_ACCOUNT_ID=${accounts[0].id} bun run provision), ` +
        `and use it for bun run deploy too. Your accounts are:\n${list}`,
    );
  }
}

function run(args: string[]): void {
  console.log("provision: wrangler " + args.join(" "));
  if (!wrangler(args, false).ok) fail("wrangler " + args[0] + " " + args[1] + " failed");
}

interface Route {
  pattern?: unknown;
  zone_name?: unknown;
  zone_id?: unknown;
  custom_domain?: unknown;
}

interface WranglerConfig {
  account_id?: unknown;
  routes?: unknown;
  r2_buckets?: { binding?: unknown; bucket_name?: unknown }[];
  queues?: { producers?: { queue?: unknown }[]; consumers?: { queue?: unknown }[] };
  vars?: Record<string, unknown>;
}

async function main(): Promise<void> {
  const path = Bun.argv[2] ?? "wrangler.jsonc";
  const file = Bun.file(path);
  if (!(await file.exists())) {
    fail(`${path} not found. Run provision in the folder of the edge Worker download of Settings > Edge delivery, or save the wrangler.jsonc of that page here.`);
  }
  let cfg: WranglerConfig;
  try {
    cfg = JSON.parse(stripJSONC(await file.text())) as WranglerConfig;
  } catch (err) {
    fail(`${path} does not parse: ${err instanceof Error ? err.message : String(err)}`);
  }
  checkLogin(process.env.CLOUDFLARE_ACCOUNT_ID || (typeof cfg.account_id === "string" ? cfg.account_id : ""));

  // Same zone: every route is a zone route of its own host (the base URL host, or a repository
  // domain). A plain pattern (what the server writes) is a route of the zone of its host, which
  // wrangler finds in the account; a route object that names its zone must name the zone of its
  // host.
  const routes = Array.isArray(cfg.routes) ? (cfg.routes as (Route | string)[]) : [];
  if (routes.length === 0) fail("the config has no routes.");
  for (const entry of routes) {
    const r: Route = typeof entry === "string" ? { pattern: entry } : entry;
    if (typeof r !== "object" || r === null || typeof r.pattern !== "string") {
      fail("each route needs a pattern.");
    }
    if (r.custom_domain === true) fail(`route ${r.pattern} is a custom domain; the Worker must run on a route of the zone.`);
    const h = r.pattern.replace(/^https?:\/\//, "").split("/")[0].toLowerCase();
    if (typeof r.zone_name === "string") {
      const zone = r.zone_name.toLowerCase();
      if (h !== zone && !h.endsWith("." + zone)) {
        fail(`route ${r.pattern} is not in zone ${zone}: a cross-zone setup breaks the client address.`);
      }
    }
  }

  // Buckets.
  const buckets = cfg.r2_buckets ?? [];
  const home = buckets.find((b) => b.binding === "HOME");
  if (!home || typeof home.bucket_name !== "string") fail("the config has no HOME bucket.");
  const homeName = home.bucket_name as string;
  if (!wrangler(["r2", "bucket", "info", homeName], true).ok) {
    fail(`the home bucket ${homeName} does not exist in this Cloudflare account, or your login cannot read it. It is the S3 bucket of the server.`);
  }
  for (const b of buckets) {
    if (typeof b.binding !== "string" || !b.binding.startsWith("R2_")) continue;
    const hint = b.binding.slice(3).toLowerCase();
    if (!HINTS.includes(hint) || typeof b.bucket_name !== "string") fail(`binding ${b.binding} names no location hint.`);
    if (wrangler(["r2", "bucket", "info", b.bucket_name], true).ok) {
      console.log(`provision: bucket ${b.bucket_name} exists`);
      continue;
    }
    run(["r2", "bucket", "create", b.bucket_name, "--location", hint]);
  }

  // Queue.
  const queue = [...(cfg.queues?.consumers ?? []), ...(cfg.queues?.producers ?? [])]
    .map((q) => q.queue)
    .find((q): q is string => typeof q === "string");
  if (!queue) fail("the config has no queue.");
  if (wrangler(["queues", "info", queue], true).ok) console.log(`provision: queue ${queue} exists`);
  else run(["queues", "create", queue]);

  // Notification rule of the home bucket.
  const raw = typeof cfg.vars?.PREFIX === "string" ? cfg.vars.PREFIX.replace(/^\/+|\/+$/g, "") : "";
  const prefix = (raw === "" ? "" : raw + "/") + "blobs/";
  const rules = wrangler(["r2", "bucket", "notification", "list", homeName], true);
  // A bucket without notification rules answers the list with error 11015, not an empty list.
  const none = !rules.ok && rules.out.includes("[code: 11015]");
  if (!rules.ok && !none) fail(`cannot list the notification rules of ${homeName}:\n${rules.out}`);
  if (!none && rules.out.includes(queue)) {
    console.log(`provision: the notification rule of ${homeName} to ${queue} exists`);
  } else {
    run([
      "r2", "bucket", "notification", "create", homeName,
      "--event-type", "object-create", "--event-type", "object-delete",
      "--queue", queue, "--prefix", prefix,
      "--description", "telesphorepo edge copies",
    ]);
  }
  console.log("provision: done. Now run `bun run deploy`.");
}

await main();
