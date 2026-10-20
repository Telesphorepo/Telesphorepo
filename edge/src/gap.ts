// The scheduled gap check (section 2 of the edge spec, "Gap check"). A run does a pass when the
// server asked for one (gap/request.json is newer than the start of the last pass) or when the
// last pass ended more than 24 hours ago. A pass lists the home blobs/ and each region in key
// order, merges the lists and queues a copy for each blob a region lacks (or holds with another
// size) and a delete for each regional blob that the home bucket lacks. It saves its cursor in
// gap/state.json and stops before 5,000 subrequests, and the next run goes on from the cursor.

import { type Config, type CopyMessage, type Env, HINTS, bucket, edgeKey, keyPrefix, readConfig, readMarker } from "./access";
import { VERSION } from "./version";

const MAX_SUBREQUESTS = 5000;
// The subrequests that a run keeps for its last writes: the last queue batch and gap/state.json.
const RESERVE = 10;
const DAY = 24 * 60 * 60;
const LIST_LIMIT = 1000;
const SEND_BATCH = 100;

// GapState is gap/state.json. Times are unix seconds.
interface GapState {
  version: string; // the Worker version that wrote it
  last_run: number; // the last run that did a pass
  bindings: string[]; // the hints of the regional buckets that this Worker has a binding for
  // The bucket that the bucket.json of each binding names ("" for none): HOME and each hint.
  markers: { home: string; regions: Record<string, string> };
  // The pass that runs now: its start, the last key done per region, the regions done, and the
  // messages queued per region so far.
  pass: { started: number; cursor: Record<string, string>; done: string[]; queued: Record<string, number> } | null;
  last_pass: { started: number; finished: number } | null;
  // Per region: when its last pass ended, and the copies and deletes that pass queued.
  regions: Record<string, { last_pass: number; queued: number }>;
}

const now = () => Math.floor(Date.now() / 1000);
const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const isStrs = (v: unknown): v is string[] => Array.isArray(v) && v.every((s) => typeof s === "string");

async function readState(env: Env): Promise<GapState> {
  const state: GapState = { version: "", last_run: 0, bindings: [], markers: { home: "", regions: {} }, pass: null, last_pass: null, regions: {} };
  const obj = await env.HOME.get(edgeKey(env, "gap/state.json"));
  if (!obj) return state;
  let v: unknown;
  try {
    v = JSON.parse(await obj.text());
  } catch {
    return state;
  }
  if (!isObj(v)) return state;
  state.version = typeof v.version === "string" ? v.version : "";
  if (isStrs(v.bindings)) state.bindings = v.bindings;
  const mk = v.markers;
  if (isObj(mk) && typeof mk.home === "string" && isObj(mk.regions)) {
    state.markers = { home: mk.home, regions: mk.regions as Record<string, string> };
  }
  if (typeof v.last_run === "number") state.last_run = v.last_run;
  const p = v.pass;
  if (isObj(p) && typeof p.started === "number") {
    state.pass = {
      started: p.started,
      cursor: isObj(p.cursor) ? (p.cursor as Record<string, string>) : {},
      done: Array.isArray(p.done) ? (p.done as string[]) : [],
      queued: isObj(p.queued) ? (p.queued as Record<string, number>) : {},
    };
  }
  const lp = v.last_pass;
  if (isObj(lp) && typeof lp.started === "number" && typeof lp.finished === "number") {
    state.last_pass = { started: lp.started, finished: lp.finished };
  }
  if (isObj(v.regions)) state.regions = v.regions as GapState["regions"];
  return state;
}

// Budget counts the subrequests of a run.
class Budget {
  used = 0;
  // take counts n subrequests; false when they do not fit. last may use the reserve.
  take(n = 1, last = false): boolean {
    if (this.used + n > MAX_SUBREQUESTS - (last ? 0 : RESERVE)) return false;
    this.used += n;
    return true;
  }
}

// gap is the cron run. Every run reads the bucket.json of each binding and writes gap/state.json
// when the version, the bindings or the buckets they name differ from the stored ones, so the
// status page sees a deploy at the next run; a run that does a pass writes it with its cursor.
export async function gap(_controller: ScheduledController, env: Env, _ctx: ExecutionContext): Promise<void> {
  const budget = new Budget();
  budget.take(); // gap/state.json
  const state = await readState(env);
  const bindings = HINTS.filter((h) => bucket(env, "R2_" + h.toUpperCase()) !== null);
  const markers: GapState["markers"] = { home: "", regions: {} };
  budget.take(1 + bindings.length);
  markers.home = await readMarker(env, env.HOME);
  for (const h of bindings) markers.regions[h] = await readMarker(env, bucket(env, "R2_" + h.toUpperCase())!);
  const stale =
    state.version !== VERSION ||
    state.bindings.join(",") !== bindings.join(",") ||
    JSON.stringify(state.markers) !== JSON.stringify(markers);
  state.version = VERSION;
  state.bindings = bindings;
  state.markers = markers;
  const save = async () => {
    budget.take(1, true);
    await env.HOME.put(edgeKey(env, "gap/state.json"), JSON.stringify(state), {
      httpMetadata: { contentType: "application/json" },
    });
  };
  let due = state.pass !== null || state.last_pass === null || now() - state.last_pass.finished > DAY;
  if (!due && state.last_pass) {
    budget.take();
    const req = await env.HOME.head(edgeKey(env, "gap/request.json"));
    due = req !== null && Math.floor(req.uploaded.getTime() / 1000) >= state.last_pass.started;
  }
  let config: Config | null = null;
  if (due) {
    budget.take();
    config = await readConfig(env);
  }
  if (!config || !config.enabled || config.version !== VERSION || !env.COPY) {
    if (stale) await save();
    return;
  }
  await pass(env, config, state, env.COPY, budget);
  state.last_run = now();
  await save();
}

async function pass(env: Env, config: Config, state: GapState, q: Queue<CopyMessage>, budget: Budget): Promise<void> {
  const p = state.pass ?? { started: now(), cursor: {}, done: [], queued: {} };
  state.pass = p;
  const queue = new Sender(q, budget);
  let all = true;
  for (const r of config.regions) {
    if (p.done.includes(r.hint)) continue;
    const r2 = bucket(env, r.binding);
    // A binding to another bucket than the region's (its bucket.json) is not the region.
    if (!r2 || r2 === env.HOME || state.markers.regions[r.hint] !== r.bucket) continue;
    const before = queue.sent;
    const finished = await merge(env, r.hint, r2, p.cursor, queue, budget);
    p.queued[r.hint] = (p.queued[r.hint] ?? 0) + (queue.sent - before);
    if (!finished) {
      all = false;
      break;
    }
    state.regions[r.hint] = { last_pass: now(), queued: p.queued[r.hint] };
    delete p.cursor[r.hint];
    delete p.queued[r.hint];
    p.done.push(r.hint);
  }
  if (all) {
    state.last_pass = { started: p.started, finished: now() };
    state.pass = null;
  }
}

// Sender batches queue messages, 100 per send.
class Sender {
  sent = 0;
  private batch: CopyMessage[] = [];

  constructor(
    private readonly q: Queue<CopyMessage>,
    private readonly budget: Budget,
  ) {}

  push(m: CopyMessage): void {
    this.batch.push(m);
  }

  get full(): boolean {
    return this.batch.length >= SEND_BATCH;
  }

  // flush sends the batch; false when the budget cannot take the send, and the batch is dropped
  // (the cursor did not move past it, so the next run queues it again).
  async flush(): Promise<boolean> {
    const b = this.batch;
    this.batch = [];
    if (b.length === 0) return true;
    if (!this.budget.take(1, true)) return false;
    await this.q.sendBatch(b.map((body) => ({ body })));
    this.sent += b.length;
    return true;
  }
}

// List pages one bucket's blobs/ in key order from a cursor.
class List {
  private buf: R2Object[] = [];
  private end = false;

  constructor(
    private readonly r2: R2Bucket,
    private readonly prefix: string,
    private after: string,
    private readonly budget: Budget,
  ) {}

  // head returns the next object, undefined at the end, or null when the budget is spent.
  async head(): Promise<R2Object | undefined | null> {
    if (this.buf.length > 0) return this.buf[0];
    if (this.end) return undefined;
    if (!this.budget.take()) return null;
    const opts: R2ListOptions = { prefix: this.prefix, limit: LIST_LIMIT };
    if (this.after !== "") opts.startAfter = this.after;
    const res = await this.r2.list(opts);
    this.buf = res.objects.slice();
    if (!res.truncated) this.end = true;
    if (this.buf.length > 0) this.after = this.buf[this.buf.length - 1].key;
    return this.buf.length > 0 ? this.buf[0] : undefined;
  }

  pop(): void {
    this.buf.shift();
  }
}

// merge walks the home blobs and the region's blobs from the region's cursor. It returns true
// when both lists end, false when the budget ran out. The cursor moves only when the messages
// up to it are sent.
async function merge(
  env: Env,
  hint: string,
  r2: R2Bucket,
  cursor: Record<string, string>,
  queue: Sender,
  budget: Budget,
): Promise<boolean> {
  const prefix = keyPrefix(env) + "blobs/";
  const start = cursor[hint] ?? "";
  const home = new List(env.HOME, prefix, start, budget);
  const region = new List(r2, prefix, start, budget);
  let latest = start;
  const commit = async (): Promise<boolean> => {
    if (!(await queue.flush())) return false;
    if (latest !== "") cursor[hint] = latest;
    return true;
  };
  for (;;) {
    const h = await home.head();
    const r = await region.head();
    if (h === null || r === null) {
      await commit();
      return false;
    }
    if (h === undefined && r === undefined) return commit();
    if (r === undefined || (h !== undefined && h.key < r.key)) {
      latest = h!.key;
      queue.push({ key: latest, hint }); // the region lacks it
      home.pop();
    } else if (h === undefined || r.key < h.key) {
      latest = r.key;
      queue.push({ key: latest, hint }); // the home bucket lacks it: the consumer deletes the copy
      region.pop();
    } else {
      latest = h.key;
      if (h.size !== r.size) queue.push({ key: latest, hint });
      home.pop();
      region.pop();
    }
    if (queue.full && !(await commit())) return false;
  }
}
