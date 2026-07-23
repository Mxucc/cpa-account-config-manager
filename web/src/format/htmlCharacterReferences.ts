const namedCharacterReferences: Record<string, string> = {
  amp: "&",
  apos: "'",
  gt: ">",
  lt: "<",
  quot: "\"",
};

const characterReferencePattern = /&(?:#([0-9]{1,7})|#[xX]([0-9a-fA-F]{1,6})|(quot|amp|apos|lt|gt));/g;

function isSafeTextCodePoint(codePoint: number): boolean {
  if (!Number.isInteger(codePoint) || codePoint <= 0 || codePoint > 0x10ffff) return false;
  if (codePoint >= 0xd800 && codePoint <= 0xdfff) return false;
  if (codePoint < 0x20 && codePoint !== 0x09 && codePoint !== 0x0a && codePoint !== 0x0d) return false;
  return codePoint < 0x7f || codePoint > 0x9f;
}

export function decodeHTMLCharacterReferences(value: string): string {
  if (!value.includes("&")) return value;
  return value.replace(characterReferencePattern, (match, decimal: string | undefined, hexadecimal: string | undefined, named: string | undefined) => {
    if (named) return namedCharacterReferences[named] || match;
    const codePoint = Number.parseInt(decimal || hexadecimal || "", decimal ? 10 : 16);
    return isSafeTextCodePoint(codePoint) ? String.fromCodePoint(codePoint) : match;
  });
}
