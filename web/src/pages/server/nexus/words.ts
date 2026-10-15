import type { NexusStatus, NexusSync } from "$lib/api";
import type { Tone } from "$lib/components/StatusBadge.svelte";
import { formatCount, formatSize } from "$lib/format";
import { count } from "$lib/words";

/** The word and the badge tone of a sync state, or of the state of one repository in a sync. */
export const syncState: Record<string, { word: string; tone: Tone }> = {
  pending: { word: "Waiting", tone: "neutral" },
  running: { word: "Running", tone: "info" },
  done: { word: "Done", tone: "success" },
  failed: { word: "Failed", tone: "danger" },
  cancelled: { word: "Cancelled", tone: "neutral" },
};

/** The badge of a Nexus server's status. */
export const sourceStatus: Record<NexusStatus, { word: string; tone: Tone }> = {
  no_repos: { word: "Choose repositories", tone: "warning" },
  syncing: { word: "Syncing", tone: "info" },
  up_to_date: { word: "Up to date", tone: "success" },
  failed: { word: "Last sync failed", tone: "danger" },
  cut_over: { word: "Switched over", tone: "success" },
};

// Nexus format ids and the words for them. An id that is not here shows with a capital letter.
const formatWords: Record<string, string> = {
  maven2: "Maven",
  apt: "Debian",
  raw: "Raw",
  npm: "npm",
  docker: "Docker",
  pypi: "PyPI",
  nuget: "NuGet",
  rubygems: "RubyGems",
  helm: "Helm",
  go: "Go",
  yum: "Yum",
  conda: "Conda",
  conan: "Conan",
  cocoapods: "CocoaPods",
  gitlfs: "Git LFS",
  bower: "Bower",
  p2: "P2",
  r: "R",
  cargo: "Cargo",
  composer: "Composer",
  huggingface: "Hugging Face",
};

/** "maven2" → "Maven". */
export const formatWord = (id: string) => formatWords[id] ?? (id ? id[0].toUpperCase() + id.slice(1) : "Unknown format");

/** "Maven, hosted"; "Debian, proxy"; "Maven, group". */
export const kindText = (format: string, type: string) => (type ? `${formatWord(format)}, ${type}` : formatWord(format));

/** How a sync started, for the history: "Saved", "Manual by sam", "Scheduled", "Final". */
export function howText(s: NexusSync): string {
  switch (s.trigger) {
    case "save":
      return "Saved";
    case "manual":
      return s.started_by ? `Manual by ${s.started_by}` : "Manual";
    case "schedule":
      return "Scheduled";
    case "cutover":
      return "Final";
  }
}

/** "Copying oasis-releases (4 of 11). 1,240 files, 84 MiB so far." */
export function progressLine(s: NexusSync): string {
  const sofar = `${count(s.imported, "file", "files")}, ${formatSize(s.bytes)} so far.`;
  if (!s.current_repo) return `Copying. ${sofar}`;
  const at = Math.min(s.repos_done + 1, Math.max(s.repo_count, 1));
  return `Copying ${s.current_repo} (${formatCount(at)} of ${formatCount(s.repo_count)}). ${sofar}`;
}

/** A length of time in words: "40 seconds", "6 minutes", "2 hours 5 minutes". */
export function durationText(ms: number): string {
  const sec = Math.max(0, Math.round(ms / 1000));
  if (sec < 60) return count(sec, "second", "seconds");
  const min = Math.round(sec / 60);
  if (min < 60) return count(min, "minute", "minutes");
  const h = Math.floor(min / 60);
  const m = min % 60;
  return m ? `${count(h, "hour", "hours")} ${count(m, "minute", "minutes")}` : count(h, "hour", "hours");
}

/** The sync interval after "every": "15 minutes", "hour", "2 hours". */
export function everyText(seconds: number): string {
  if (seconds % 3600 === 0) return seconds === 3600 ? "hour" : count(seconds / 3600, "hour", "hours");
  if (seconds % 60 === 0) return seconds === 60 ? "minute" : count(seconds / 60, "minute", "minutes");
  return count(seconds, "second", "seconds");
}
