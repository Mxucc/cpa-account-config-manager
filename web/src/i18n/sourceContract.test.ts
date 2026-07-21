import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import { enUI } from "./uiCatalogEn";
import { ruUI } from "./uiCatalogRu";
import { zhCNUI } from "./uiCatalogZhCN";
import { zhTWUI } from "./uiCatalogZhTW";

const localizedSourceFiles = new Set([
  "src/i18n/messages.ts",
  "src/i18n/operatorText.ts",
  "src/i18n/uiCatalogRu.ts",
  "src/i18n/uiCatalogZhCN.ts",
  "src/i18n/uiCatalogZhTW.ts",
]);

const ignoredSourceDirectories = new Set(["node_modules", ".git", ".codex-tasks"]);

function sourceFiles(root: string, extensions: Set<string>): string[] {
  const files: string[] = [];
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    if (entry.isDirectory() && ignoredSourceDirectories.has(entry.name)) continue;
    const path = join(root, entry.name);
    if (entry.isDirectory()) files.push(...sourceFiles(path, extensions));
    else if (extensions.has(entry.name.slice(entry.name.lastIndexOf(".")))) files.push(path);
  }
  return files;
}

describe("English-source localization contract", () => {
  it("uses stable English semantic IDs with complete locale catalogs", () => {
    const expected = Object.keys(enUI).sort();
    expect(expected.every((key) => /^ui\.[a-z0-9_]+$/.test(key))).toBe(true);
    expect(Object.keys(zhCNUI).sort()).toEqual(expected);
    expect(Object.keys(zhTWUI).sort()).toEqual(expected);
    expect(Object.keys(ruUI).sort()).toEqual(expected);
  });

  it("keeps localized display text out of frontend runtime logic", () => {
    const root = process.cwd();
    const violations = sourceFiles(join(root, "src"), new Set([".ts", ".tsx"]))
      .filter((path) => !path.includes(".test."))
      .filter((path) => !localizedSourceFiles.has(relative(root, path)))
      .filter((path) => /[\u3400-\u9fff]/u.test(readFileSync(path, "utf8")))
      .map((path) => relative(root, path));
    expect(violations).toEqual([]);
  });

  it("does not branch on locale in frontend runtime code", () => {
    const root = process.cwd();
    const violations = sourceFiles(join(root, "src"), new Set([".ts", ".tsx"]))
      .filter((path) => !path.includes(".test."))
      .filter((path) => /\blocale\s*[!=]==?/.test(readFileSync(path, "utf8")))
      .map((path) => relative(root, path));
    expect(violations).toEqual([]);
  });

  it("keeps backend Go source English-only", () => {
    const root = process.cwd();
    const violations = sourceFiles(join(root, ".."), new Set([".go"]))
      .filter((path) => !path.endsWith("_test.go"))
      .filter((path) => !path.includes(`${join("web", "node_modules")}`))
      .filter((path) => /[\u3400-\u9fff]/u.test(readFileSync(path, "utf8")))
      .map((path) => relative(join(root, ".."), path));
    expect(violations).toEqual([]);
  });
});
