import { NextResponse } from "next/server";
import type { Dirent } from "fs";
import { readdir, readFile } from "fs/promises";
import { join } from "path";
import { homedir } from "os";

// Reads ~/.multica/plugins/*/plugin.json + state.json and returns the plugin list.
// This is a local API — only the self-hosted instance serving localhost can call it.
export async function GET() {
  const pluginsDir = join(homedir(), ".multica", "plugins");

  let entries: Dirent<string>[];
  try {
    entries = await readdir(pluginsDir, { encoding: "utf8", withFileTypes: true });
  } catch {
    return NextResponse.json([]);
  }

  const plugins: Array<{ manifest: Record<string, unknown>; enabled: boolean }> = [];

  for (const entry of entries) {
    if (!entry.isDirectory()) continue;

    const manifestPath = join(pluginsDir, entry.name, "plugin.json");
    let manifest: Record<string, unknown>;
    try {
      manifest = JSON.parse(await readFile(manifestPath, "utf-8"));
    } catch {
      continue;
    }

    let enabled = true;
    try {
      const state = JSON.parse(
        await readFile(join(pluginsDir, entry.name, "state.json"), "utf-8"),
      );
      enabled = state.enabled !== false;
    } catch {
      // missing state.json → default enabled
    }

    plugins.push({ manifest, enabled });
  }

  return NextResponse.json(plugins);
}
