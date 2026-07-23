import { describe, expect, it } from "vitest";
import { decodeHTMLCharacterReferences } from "./htmlCharacterReferences";

describe("decodeHTMLCharacterReferences", () => {
  it("decodes decimal, hexadecimal, and the bounded named set", () => {
    expect(decodeHTMLCharacterReferences("&#34;error&#34; &#x22;type&#X22; &amp; &lt;ok&gt; &apos;x&apos;"))
      .toBe('"error" "type" & <ok> \'x\'');
  });

  it("preserves unsupported, invalid, and unsafe references", () => {
    const value = "&copy; &#0; &#1; &#127; &#xD800; &#1114112; &#12345678;";
    expect(decodeHTMLCharacterReferences(value)).toBe(value);
  });

  it("performs exactly one decoding pass", () => {
    expect(decodeHTMLCharacterReferences("&amp;#34;message&amp;#34;")).toBe("&#34;message&#34;");
  });

  it("leaves ordinary response text unchanged", () => {
    const value = "The usage limit has been reached";
    expect(decodeHTMLCharacterReferences(value)).toBe(value);
  });
});
