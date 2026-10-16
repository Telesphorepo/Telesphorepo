// The UI of the maven module: its part of the details of a pom or a jar (file_details of the
// format's descriptor).

import type { ModuleUI } from "../types";

export const maven: ModuleUI = {
  id: "maven",
  formatParts: { maven: { "maven-artifact": () => import("./ArtifactPart.svelte") } },
};
