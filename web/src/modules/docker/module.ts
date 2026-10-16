// The UI of the docker module: its part of the details of a tag or a manifest (file_details of the
// format's descriptor).

import type { ModuleUI } from "../types";

export const docker: ModuleUI = {
  id: "docker",
  formatParts: { docker: { manifest: () => import("./ManifestPart.svelte") } },
};
