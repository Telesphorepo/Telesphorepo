import { api, ApiError, type PublishSession } from "$lib/api";

/** Renew the session while the upload runs. Stop renewal before closing the session. */
export async function keepSession(
  session: PublishSession,
  key: string,
  upload: AbortSignal,
  ended: () => void,
): Promise<() => void> {
  let deadline = Infinity;
  let delay = 0;
  let timer: ReturnType<typeof setTimeout>;
  const ctl = new AbortController();
  const stop = () => {
    clearTimeout(timer);
    ctl.abort();
    upload.removeEventListener("abort", stop);
  };
  const renew = async () => {
    const start = performance.now();
    const timeout = Math.min(10_000, Math.max(1, deadline - start));
    const signal = AbortSignal.any([ctl.signal, AbortSignal.timeout(Math.floor(timeout))]);
    const next = await api.publish.renewSession(session.id, session.token, key, signal);
    deadline = start + next.valid_for_ms;
    delay = next.renew_after_ms;
    if (!(delay > 0 && performance.now() < deadline)) throw new Error("The publish session expired.");
  };
  const schedule = () => {
    timer = setTimeout(tick, Math.max(0, Math.min(delay, deadline - performance.now())));
  };
  const tick = async () => {
    try {
      await renew();
    } catch (e) {
      if (ctl.signal.aborted) return;
      if (performance.now() >= deadline || (e instanceof ApiError && e.status >= 400 && e.status < 500)) {
        stop();
        ended();
        return;
      }
      delay = Math.min(delay, 2_000);
    }
    if (!ctl.signal.aborted) schedule();
  };
  if (upload.aborted) throw new DOMException("The upload was cancelled.", "AbortError");
  upload.addEventListener("abort", stop, { once: true });
  try {
    await renew();
  } catch (e) {
    stop();
    throw e;
  }
  if (!ctl.signal.aborted) schedule();
  return stop;
}
