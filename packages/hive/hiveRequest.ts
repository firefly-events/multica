// Shared fetch helper for all Hive plugin views.
//
// Mirrors the CSRF handling in packages/core/api/client.ts: Multica's auth
// middleware rejects mutations (POST/PATCH) that do not carry the
// X-CSRF-Token header read from the `multica_csrf` cookie. Each view used to
// define its own helper that omitted this token, so every Hive write returned
// 403. Consolidated here so the token is always attached.

function readCsrfToken(): string | null {
  if (typeof document === "undefined") return null;
  const match = document.cookie
    .split("; ")
    .find((c) => c.startsWith("multica_csrf="));
  return match ? (match.split("=")[1] ?? null) : null;
}

export async function hiveRequest(
  path: string,
  wsId: string,
  options?: RequestInit,
) {
  const csrf = readCsrfToken();
  const res = await fetch(`/api/plugins/hive${path}`, {
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      "X-Workspace-ID": wsId,
      ...(csrf ? { "X-CSRF-Token": csrf } : {}),
      ...options?.headers,
    },
    ...options,
  });
  if (!res.ok) throw new Error(`hive ${path} ${res.status}`);
  return res.json();
}
