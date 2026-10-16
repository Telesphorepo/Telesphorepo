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
  "None yet. Run the login command of the publisher client of your build tool, then approve the code that it prints.";

export const clientName = (d: Device) => d.client_title;

/** A publish token's state: Active (open), Ended (closed) or Expired. */
export function tokenState(s: SessionInfo): { word: string; tone: Tone } {
  if (s.state === "open") return { word: "Active", tone: "success" };
  if (s.state === "closed") return { word: "Ended", tone: "neutral" };
  return { word: "Expired", tone: "neutral" };
}

/** Where a publish token came from. */
export const tokenSource = (s: SessionInfo) =>
  s.via === "device" ? `Build tool on ${s.device_name ?? "a device"}` : s.via === "oidc" ? `CI pipeline ${s.trust_name ?? ""}`.trim() : "This site";
