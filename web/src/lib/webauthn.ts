// Browser side of WebAuthn, through @simplewebauthn/browser. The server (go-webauthn) sends the
// options as { publicKey } in the standard JSON form and takes the credential back in that form.

import {
  browserSupportsWebAuthn,
  startAuthentication,
  startRegistration,
  type PublicKeyCredentialCreationOptionsJSON,
  type PublicKeyCredentialRequestOptionsJSON,
} from "@simplewebauthn/browser";
import type { PasskeyOptions } from "$lib/api";

export const passkeysSupported = () => typeof window !== "undefined" && browserSupportsWebAuthn();

/** Creates a passkey from the options of POST /api/security/passkeys/begin. */
export const createPasskey = (options: PasskeyOptions) =>
  startRegistration({ optionsJSON: options.publicKey as unknown as PublicKeyCredentialCreationOptionsJSON });

/** Signs a passkey check from the options of a login or assert route. */
export const getPasskeyAssertion = (options: PasskeyOptions) =>
  startAuthentication({ optionsJSON: options.publicKey as unknown as PublicKeyCredentialRequestOptionsJSON });
