// The queue consumer: it copies blobs from the home bucket to the regions and deletes regional
// copies (section 2 of the edge spec, "Blob copies"). A message names a blob key: an R2 event
// notification of the home bucket, or a gap-check message for one region. The consumer acts on
// the current state of the home bucket, never on the event alone, so duplicate and out-of-order
// messages are safe.

import { type Config, type CopyMessage, type Env, bucket, edgeKey, keyPrefix, limited, readConfig } from "./access";

const MiB = 1024 * 1024;
// Above this size a copy uses a multipart upload (a single put takes at most 4.995 GiB).
const SINGLE_MAX = 4.9 * 1024 * MiB;
// A consumer run lasts at most 15 minutes of wall time; a multipart copy stops in time to save
// its state.
const RUN_BUDGET_MS = 12 * 60 * 1000;
// The messages of a batch copied at once. The runtime holds at most six requests that wait for
// response headers and queues the rest; a request that streams its body does not count, so many
// copies run at once and the run waits on R2, not on this number.
const COPIES = 32;

interface Target {
  hint: string;
  r2: R2Bucket;
}

// regions returns the regions to copy to: ready or filling, with a binding in this Worker.
function regions(env: Env, config: Config, hint?: string): Target[] {
  const out: Target[] = [];
  for (const r of config.regions) {
    if (hint !== undefined && r.hint !== hint) continue;
    const r2 = bucket(env, r.binding);
    if (r2 && r2 !== env.HOME) out.push({ hint: r.hint, r2 });
  }
  return out;
}

export async function copy(batch: MessageBatch<CopyMessage>, env: Env, _ctx: ExecutionContext): Promise<void> {
  const config = await readConfig(env);
  if (!config) {
    batch.retryAll({ delaySeconds: 60 });
    return;
  }
  const deadline = Date.now() + RUN_BUDGET_MS;
  await limited(batch.messages, COPIES, async (msg) => {
    try {
      const done = await handle(env, config, msg.body, deadline);
      if (done) msg.ack();
      else msg.retry({ delaySeconds: 0 });
    } catch (err) {
      console.error("edge copy: retry:", err instanceof Error ? err.message : String(err));
      msg.retry({ delaySeconds: 30 });
    }
  });
}

// handle acts on one message; false: a multipart copy saved its state and goes on in the next run.
async function handle(env: Env, config: Config, body: CopyMessage, deadline: number): Promise<boolean> {
  const key = typeof body?.object?.key === "string" ? body.object.key : typeof body?.key === "string" ? body.key : "";
  const m = new RegExp("^" + escape(keyPrefix(env)) + "blobs/sha256/([0-9a-f]{2})/([0-9a-f]{64})$").exec(key);
  if (!m || !m[2].startsWith(m[1])) return true; // not a blob
  const hex = m[2];
  const targets = regions(env, config, typeof body?.hint === "string" ? body.hint : undefined);
  if (targets.length === 0) return true;
  const home = await env.HOME.head(key);
  // Every region at once. Each one finishes its copy or delete; a failed one throws its error
  // afterwards, so the message is retried and the regions that have the blob skip it then.
  const results = await Promise.allSettled(
    targets.map(async (t) => {
      if (home) {
        const have = await t.r2.head(key);
        if (have && have.size === home.size) return true;
        return copyTo(env, t, key, hex, home.size, deadline);
      }
      await t.r2.delete(key);
      await dropState(env, t, key, hex);
      return true;
    }),
  );
  let done = true;
  for (const r of results) {
    if (r.status === "rejected") throw r.reason;
    done = done && r.value;
  }
  return done;
}

function escape(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&");
}

// fixed gives a stream a known length, which an R2 put and uploadPart need.
function fixed(body: ReadableStream, length: number): ReadableStream {
  const { readable, writable } = new FixedLengthStream(length);
  body.pipeTo(writable).catch(() => {
    // The put fails on a short stream, and the message retries.
  });
  return readable;
}

// copyTo streams the home blob into the region. false: a multipart copy ran out of time and
// saved its state.
async function copyTo(env: Env, t: Target, key: string, hex: string, size: number, deadline: number): Promise<boolean> {
  if (size <= SINGLE_MAX) {
    const src = await env.HOME.get(key);
    if (!src) return true; // deleted meanwhile; its delete event removes the copies
    await t.r2.put(key, fixed(src.body, src.size), { sha256: hex });
    return true;
  }
  return multipart(env, t, key, hex, size, deadline);
}

// The state of a multipart copy, in copy/<hint>/<hex>.json of the home bucket.
interface CopyState {
  upload: string;
  size: number;
  part_size: number;
  parts: R2UploadedPart[];
}

const stateKey = (env: Env, t: Target, hex: string) => edgeKey(env, `copy/${t.hint}/${hex}.json`);

async function readState(env: Env, t: Target, hex: string): Promise<CopyState | null> {
  const obj = await env.HOME.get(stateKey(env, t, hex));
  if (!obj) return null;
  try {
    const v = JSON.parse(await obj.text()) as CopyState;
    if (typeof v.upload !== "string" || typeof v.size !== "number" || typeof v.part_size !== "number" || !Array.isArray(v.parts)) {
      return null;
    }
    return v;
  } catch {
    return null;
  }
}

async function dropState(env: Env, t: Target, key: string, hex: string): Promise<void> {
  const st = await readState(env, t, hex);
  if (!st) return;
  try {
    await t.r2.resumeMultipartUpload(key, st.upload).abort();
  } catch {
    // R2 aborts an unfinished upload after 7 days anyway.
  }
  await env.HOME.delete(stateKey(env, t, hex));
}

function noSuchUpload(err: unknown): boolean {
  const e = err as { code?: number; message?: string };
  return e?.code === 10024 || /NoSuchUpload/i.test(e?.message ?? "");
}

// multipart copies a blob above SINGLE_MAX in parts of equal size: the larger of 100 MiB and the
// size divided by 10,000, rounded up to a whole MiB. The state is saved after each part, so a
// retried message resumes the upload.
async function multipart(env: Env, t: Target, key: string, hex: string, size: number, deadline: number): Promise<boolean> {
  const partSize = Math.max(100 * MiB, Math.ceil(size / 10000 / MiB) * MiB);
  const count = Math.ceil(size / partSize);
  let st = await readState(env, t, hex);
  let upload: R2MultipartUpload;
  if (st && st.size === size && st.part_size === partSize) {
    upload = t.r2.resumeMultipartUpload(key, st.upload);
  } else {
    if (st) await dropState(env, t, key, hex);
    upload = await t.r2.createMultipartUpload(key);
    st = { upload: upload.uploadId, size, part_size: partSize, parts: [] };
    await saveState(env, t, hex, st);
  }
  const state = st;
  try {
    for (let n = 1; n <= count; n++) {
      if (state.parts.some((p) => p.partNumber === n)) continue;
      if (Date.now() > deadline) return false;
      const offset = (n - 1) * partSize;
      const length = Math.min(partSize, size - offset);
      const src = await env.HOME.get(key, { range: { offset, length } });
      if (!src) {
        await dropState(env, t, key, hex); // deleted meanwhile
        return true;
      }
      state.parts.push(await upload.uploadPart(n, fixed(src.body, length)));
      await saveState(env, t, hex, state);
    }
    await upload.complete(state.parts.slice().sort((a, b) => a.partNumber - b.partNumber));
  } catch (err) {
    if (noSuchUpload(err)) await env.HOME.delete(stateKey(env, t, hex)); // start again on retry
    throw err;
  }
  await env.HOME.delete(stateKey(env, t, hex));
  return true;
}

async function saveState(env: Env, t: Target, hex: string, st: CopyState): Promise<void> {
  await env.HOME.put(stateKey(env, t, hex), JSON.stringify(st), { httpMetadata: { contentType: "application/json" } });
}
