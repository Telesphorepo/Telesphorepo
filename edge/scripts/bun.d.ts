// The part of the Bun runtime that the scripts use. The Worker's types (workers-types) and Bun's
// own types cannot share one program, so the scripts declare what they need here.

interface BunSpawnResult {
  exitCode: number | null;
  stdout: Uint8Array | null;
  stderr: Uint8Array | null;
}

declare const Bun: {
  argv: string[];
  spawnSync(
    cmd: string[],
    options?: { stdout?: "pipe" | "inherit"; stderr?: "pipe" | "inherit"; stdin?: "inherit" | "ignore"; cwd?: string },
  ): BunSpawnResult;
  file(path: string): { text(): Promise<string>; exists(): Promise<boolean> };
};
