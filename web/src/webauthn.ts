// Browser side of WebAuthn. The server sends and expects the standard JSON form, where binary
// fields are base64url strings.

const b64uToBuf = (s: string): ArrayBuffer => {
  const b = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
  const out = new Uint8Array(b.length);
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i);
  return out.buffer;
};

const bufToB64u = (buf: ArrayBuffer): string => {
  let s = "";
  for (const c of new Uint8Array(buf)) s += String.fromCharCode(c);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
};

type Descriptor = { id: string; type: string; transports?: string[] };
type ServerOptions = { publicKey: Record<string, unknown> & { challenge: string; user?: { id: string }; excludeCredentials?: Descriptor[]; allowCredentials?: Descriptor[] } };

const descriptors = (list?: Descriptor[]) =>
  list?.map((d) => ({ ...d, id: b64uToBuf(d.id) })) as PublicKeyCredentialDescriptor[] | undefined;

export const passkeysSupported = () => typeof window !== "undefined" && !!window.PublicKeyCredential && !!navigator.credentials;

export async function createPasskey(options: ServerOptions): Promise<unknown> {
  const pk = options.publicKey;
  const cred = (await navigator.credentials.create({
    publicKey: {
      ...pk,
      challenge: b64uToBuf(pk.challenge),
      user: { ...(pk.user as object), id: b64uToBuf(pk.user!.id) },
      excludeCredentials: descriptors(pk.excludeCredentials),
    } as PublicKeyCredentialCreationOptions,
  })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was created.");
  const r = cred.response as AuthenticatorAttestationResponse;
  return {
    id: cred.id,
    rawId: bufToB64u(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      attestationObject: bufToB64u(r.attestationObject),
      clientDataJSON: bufToB64u(r.clientDataJSON),
      transports: r.getTransports?.() ?? [],
    },
  };
}

export async function getPasskeyAssertion(options: ServerOptions): Promise<unknown> {
  const pk = options.publicKey;
  const cred = (await navigator.credentials.get({
    publicKey: {
      ...pk,
      challenge: b64uToBuf(pk.challenge),
      allowCredentials: descriptors(pk.allowCredentials),
    } as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential | null;
  if (!cred) throw new Error("No passkey was selected.");
  const r = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: bufToB64u(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      authenticatorData: bufToB64u(r.authenticatorData),
      clientDataJSON: bufToB64u(r.clientDataJSON),
      signature: bufToB64u(r.signature),
      userHandle: r.userHandle ? bufToB64u(r.userHandle) : undefined,
    },
  };
}
