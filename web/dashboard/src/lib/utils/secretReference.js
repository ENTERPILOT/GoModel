// A secret reference, ${scheme:reference}, names where the gateway reads a
// secret from (an environment variable, a mounted file, a secret manager).
// It is not secret itself: the gateway returns it unmasked, and the editors
// show it as plain text. "$${" is the escape for a literal "${".
const SECRET_REFERENCE = /(?<!\$)\$\{[a-z][a-z0-9+.-]*:[^{}-][^{}]*\}/;

// hasSecretReference reports whether value holds at least one secret
// reference, matching the gateway's rule.
export function hasSecretReference(value) {
  return SECRET_REFERENCE.test(String(value ?? ""));
}

const ONLY_SECRET_REFERENCES = /^(?:\$\{[a-z][a-z0-9+.-]*:[^{}-][^{}]*\})+$/;

// onlySecretReferences reports whether value is made of secret references
// alone. Only such a value is shown in clear: one that mixes literal text
// with a reference may hold a literal secret, and the gateway masks it.
export function onlySecretReferences(value) {
  return ONLY_SECRET_REFERENCES.test(String(value ?? ""));
}
