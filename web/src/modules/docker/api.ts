// The calls to the routes of the Docker module.
import { request } from "$lib/api/client";

/** A blob or a manifest that a manifest names. */
export type OCIDescriptor = { digest: string; media_type: string; size: number };
/** A manifest of an index with its platform. */
export type OCIPlatform = { digest: string; os: string; architecture: string; variant?: string };
/** GET /api/docker/{name}/manifest?path=: the details of a manifest at a tag or manifest path. */
export type DockerManifest = {
  digest: string;
  media_type: string;
  /** The artifactType, else the config type unless it is the OCI empty config; "" for none. */
  artifact_type: string;
  size: number;
  /** The manifest with its config and layers; an index counts only itself. */
  total_size: number;
  config: OCIDescriptor | null;
  layers: OCIDescriptor[];
  /** The platform manifests of an index; empty for an image manifest. */
  platforms: OCIPlatform[];
  subject: OCIDescriptor | null;
  /** The first 100 tags that name the manifest. */
  tags: string[];
  /** How many manifests name it as their subject (signatures, SBOMs, attestations). */
  referrers: number;
};

export const dockerApi = {
  /** The details of the manifest at a tag or manifest path of a docker repo. */
  manifest: (name: string, path: string) =>
    request<DockerManifest>("GET", `/api/docker/${encodeURIComponent(name)}/manifest`, { query: { path } }),
};
