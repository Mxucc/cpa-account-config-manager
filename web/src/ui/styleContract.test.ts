import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, test } from "vitest";

// Source contract for the Corporate Clean design system. It cannot judge taste,
// but it can stop the concrete drifts the style guide forbids: sharp corners,
// heavy shadows, gradient buttons, neon text and slow motion.
const roots = [resolve(__dirname), resolve(__dirname, "../v2")];

function* walk(directory: string): Generator<string> {
  for (const entry of readdirSync(directory)) {
    const full = join(directory, entry);
    if (statSync(full).isDirectory()) {
      yield* walk(full);
      continue;
    }
    if (/\.(tsx|ts|css)$/.test(entry) && !entry.endsWith(".test.ts") && !entry.endsWith(".test.tsx")) {
      yield full;
    }
  }
}

const sources = [...roots].flatMap((root) => [...walk(root)].map((path) => ({ path, text: readFileSync(path, "utf8") })));

const forbidden: Array<[string, RegExp]> = [
  ["rounded-none", /\brounded-none\b/],
  ["shadow-2xl", /\bshadow-2xl\b/],
  ["border-4", /\bborder-4\b/],
  ["gradient background", /\bbg-gradient-/],
  ["neon text", /\btext-neon\b/],
  ["slow motion", /\bduration-(300|500|700|1000)\b/],
];

describe("Corporate Clean source contract", () => {
  test.each(forbidden)("no %s", (_label, pattern) => {
    const offenders = sources.filter((source) => pattern.test(source.text)).map((source) => source.path);
    expect(offenders).toEqual([]);
  });

  test("buttons keep focus offset, press feedback and a 200ms cap", () => {
    const kit = readFileSync(resolve(__dirname, "kit.tsx"), "utf8");
    expect(kit).toContain("active:scale-[0.98]");
    expect(kit).toContain("active:translate-y-0");
    expect(kit).toContain("focus-visible:ring-offset-2");
    expect(kit).toContain("focus-visible:ring-2");
    expect(kit).toMatch(/duration-(150|200)/);
  });

  // One surface definition, in the token layer; the kit names it and pages must
  // not re-invent a card out of utilities.
  test("cards use the shared surface tokens", () => {
    const kit = readFileSync(resolve(__dirname, "kit.tsx"), "utf8");
    const css = readFileSync(resolve(__dirname, "../styles/v2.css"), "utf8");
    expect(css).toContain(".v2-card {");
    expect(css).toContain("--v2-pad-card");
    expect(kit).toContain("v2-card");
    // A lift is offered only to an interactive card, never to every surface.
    expect(kit).toContain("hover:-translate-y-0.5 hover:shadow-md");
  });

  test("pages compose the shared primitives instead of inline card and field styles", () => {
    // The legacy bridge embeds the old workspaces on purpose; its classes belong
    // to the old stylesheet, not to this system.
    const pages = sources.filter((source) => source.path.includes("/v2/") && !source.path.includes("legacy/"));
    const offenders = pages
      .filter((source) => /bg-white rounded-xl|rounded-xl border border-gray-200|border-dashed border-gray-300/.test(source.text))
      .map((source) => source.path);
    expect(offenders).toEqual([]);
  });

  test("inputs keep the blue focus ring with an offset", () => {
    const kit = readFileSync(resolve(__dirname, "kit.tsx"), "utf8");
    expect(kit).toContain("focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 focus:border-blue-500");
  });

  test("reduced motion has an explicit fallback", () => {
    const css = readFileSync(resolve(__dirname, "../styles/v2.css"), "utf8");
    expect(css).toContain("prefers-reduced-motion: reduce");
  });

  test("a table row and every meter are present in the account view", () => {
    const accounts = readFileSync(resolve(__dirname, "../v2/pages/Accounts.tsx"), "utf8");
    // Rows and their hover state come from the shared table tokens.
    expect(accounts).toContain("Table");
    expect(accounts).toContain("v2-truncate");
    expect(accounts).toContain("AccountQuota");
  });
});
