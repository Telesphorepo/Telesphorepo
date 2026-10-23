// The words that the account pages share for devices and publish tokens, so Overview, Devices and
// Tokens say the same thing for the same state.

import type { Device, SessionInfo } from "$lib/api";
import type { Tone } from "$lib/components/StatusBadge.svelte";

/** A device's state: Active, Expired or Revoked. */
export function deviceState(d: Device): { word: string; tone: Tone } {
  if (d.revoked_at) return { word: "Revoked", tone: "danger" };
  if (new Date(d.expires_at) < new Date()) return { word: "Expired", tone: "neutral" };
  return { word: "Active", tone: "success" };
}

/** The empty list of devices, on Overview and on Devices. */
export const noDevices =
  "None yet. Run telesphorepo login in a terminal, then approve the code that it prints.";

/** The one program that enrolls a device (the device ticket's printed line). */
export const cliName = "Telesphorepo CLI";

/** A publish token's state: Active (open), Ended (closed) or Expired. */
export function tokenState(s: SessionInfo): { word: string; tone: Tone } {
  if (s.state === "open") return { word: "Active", tone: "success" };
  if (s.state === "closed") return { word: "Ended", tone: "neutral" };
  return { word: "Expired", tone: "neutral" };
}

/** Where a publish token came from. */
export const tokenSource = (s: SessionInfo) =>
  s.via === "device" ? `Telesphorepo CLI on ${s.device_name ?? "a device"}` : s.via === "oidc" ? `CI pipeline ${s.trust_name ?? ""}`.trim() : "This site";

/** The words for the error codes that single sign-on returns to a page after a new sign-in. */
export const ssoErrors: Record<string, string> = {
  stale_sign_in:
    "Your identity provider did not ask you to sign in again: the sign-in it sent is more than 5 minutes old. The provider ignored the request for a new sign-in (prompt=login, max_age=0). Ask an administrator to let the provider force a new sign-in for this app.",
  no_auth_time:
    "Your identity provider did not say when you signed in (the ID token has no auth_time), so the server cannot check that the sign-in is new. Ask an administrator to add auth_time to the ID token.",
  wrong_account: "You signed in to your identity provider with another account. Sign in again with this account.",
  account_disabled: "This account is disabled.",
  not_sso: "You sign in with a password. Confirm with two-step verification.",
  sso_failed: "Single sign-on failed. Try again.",
};
