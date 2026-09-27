import { describe, expect, it } from "vitest";
import { creationOptions, fromB64url, requestOptions, toB64url } from "./passkeys";

describe("passkeys", () => {
  it("round-trips base64url", () => {
    const bytes = new Uint8Array([0, 1, 250, 251, 252, 253, 254, 255, 62, 63]);
    const s = toB64url(bytes);
    expect(s).not.toMatch(/[+/=]/);
    expect(new Uint8Array(fromB64url(s))).toEqual(bytes);
    expect(toB64url(null)).toBe("");
  });
  it("decodes the ids and challenges the browser needs as bytes", () => {
    const c = creationOptions({ challenge: "AAEC", rp: { id: "localhost", name: "kmdn" }, user: { id: "dXNy", name: "m", displayName: "M" }, excludeCredentials: [{ type: "public-key", id: "AQI" }], pubKeyCredParams: [] });
    expect(new Uint8Array(c.challenge as ArrayBuffer)).toEqual(new Uint8Array([0, 1, 2]));
    expect(new TextDecoder().decode(c.user.id as ArrayBuffer)).toBe("usr");
    expect(new Uint8Array(c.excludeCredentials![0]!.id as ArrayBuffer)).toEqual(new Uint8Array([1, 2]));
    const r = requestOptions({ challenge: "AAEC", rpId: "localhost" });
    expect(r.allowCredentials).toEqual([]);
    expect(r.rpId).toBe("localhost");
  });
});
