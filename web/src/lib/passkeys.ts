/** WebAuthn in the browser: options from JSON, credentials to JSON (base64url). */

type Json = Record<string, unknown>;

export function passkeysSupported(): boolean {
  return typeof window !== "undefined" && typeof window.PublicKeyCredential === "function" && !!navigator.credentials;
}

export function fromB64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

export function toB64url(b: ArrayBuffer | ArrayBufferView | null | undefined): string {
  if (!b) return "";
  const bytes = b instanceof ArrayBuffer ? new Uint8Array(b) : new Uint8Array(b.buffer, b.byteOffset, b.byteLength);
  let bin = "";
  for (const x of bytes) bin += String.fromCharCode(x);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

const withIds = (list: unknown) => ((list as Json[] | undefined) ?? []).map((c) => ({ ...c, id: fromB64url(c.id as string) }));

/** The server's creation options as the browser wants them. */
export function creationOptions(pk: Json): PublicKeyCredentialCreationOptions {
  const user = pk.user as Json;
  return {
    ...(pk as unknown as PublicKeyCredentialCreationOptions),
    challenge: fromB64url(pk.challenge as string),
    user: { ...(user as unknown as PublicKeyCredentialUserEntity), id: fromB64url(user.id as string) },
    excludeCredentials: withIds(pk.excludeCredentials) as PublicKeyCredentialDescriptor[],
  };
}

export function requestOptions(pk: Json): PublicKeyCredentialRequestOptions {
  return {
    ...(pk as unknown as PublicKeyCredentialRequestOptions),
    challenge: fromB64url(pk.challenge as string),
    allowCredentials: withIds(pk.allowCredentials) as PublicKeyCredentialDescriptor[],
  };
}

/** A credential as the server expects it (the WebAuthn JSON encoding). */
export function credentialJSON(c: PublicKeyCredential): Json {
  const r = c.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse;
  const response: Json = { clientDataJSON: toB64url(r.clientDataJSON) };
  if ("attestationObject" in r && r.attestationObject) {
    response.attestationObject = toB64url(r.attestationObject);
    response.transports = typeof r.getTransports === "function" ? r.getTransports() : [];
  } else {
    response.authenticatorData = toB64url(r.authenticatorData);
    response.signature = toB64url(r.signature);
    if (r.userHandle) response.userHandle = toB64url(r.userHandle);
  }
  return {
    id: c.id,
    rawId: toB64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    response,
    clientExtensionResults: c.getClientExtensionResults(),
  };
}

/** The person closed or cancelled the browser's passkey sheet. */
export function cancelled(e: unknown): boolean {
  return e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError");
}

export async function createPasskey(pk: Json): Promise<Json> {
  const c = (await navigator.credentials.create({ publicKey: creationOptions(pk) })) as PublicKeyCredential | null;
  if (!c) throw new DOMException("No passkey was created", "NotAllowedError");
  return credentialJSON(c);
}

export async function getPasskey(pk: Json): Promise<Json> {
  const c = (await navigator.credentials.get({ publicKey: requestOptions(pk) })) as PublicKeyCredential | null;
  if (!c) throw new DOMException("No passkey was chosen", "NotAllowedError");
  return credentialJSON(c);
}
