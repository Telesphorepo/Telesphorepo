// VERSION is the server version that this Worker belongs to: the define TELESPHOREPO_VERSION of the
// wrangler.jsonc that the server gives (and of the bundle of a release, `--define
// TELESPHOREPO_VERSION:'"v1.2.3"'`). A build without it is "dev", like a server binary built
// without -X main.version.
declare const TELESPHOREPO_VERSION: string | undefined;

export const VERSION: string = typeof TELESPHOREPO_VERSION === "string" ? TELESPHOREPO_VERSION : "dev";
