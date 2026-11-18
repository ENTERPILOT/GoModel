// Display labels for managed API keys. Key names need not be unique (a new
// key may reuse a deactivated key's name), so wherever a key is shown by name
// alone, same-name keys get their redacted token appended to tell them
// apart. Names compare exactly after trimming, as the backend stores them:
// "CI" and "ci" are different names. Free of Svelte/DOM dependencies so
// node:test can exercise it.

function keyID(key) {
  return String((key && key.id) || "").trim();
}

function keyName(key) {
  return String((key && key.name) || "").trim();
}

// keyTag is what tells a key apart from same-name keys: its redacted token,
// or a short id when the token has no visible suffix (an import without one).
function keyTag(key, id) {
  const redacted = String(key.redacted_value || "").trim();
  if (redacted && !redacted.endsWith("...")) return redacted;
  return id.slice(0, 8);
}

// authKeyLabels maps each key id to its label: the name alone when no other
// key shares it, otherwise "name (sk_gom_...abcd)". Same-name keys whose
// redacted tokens also match (the four visible characters can repeat, and an
// import supplies its own) get a short id added. A key without a name falls
// back to its id. Entries without an id are skipped.
export function authKeyLabels(keys) {
  const list = (Array.isArray(keys) ? keys : []).filter((key) => keyID(key));
  const names = new Map();
  const tags = new Map();
  for (const key of list) {
    const name = keyName(key);
    if (!name) continue;
    names.set(name, (names.get(name) || 0) + 1);
    const tagged = name + "\n" + keyTag(key, keyID(key));
    tags.set(tagged, (tags.get(tagged) || 0) + 1);
  }
  const labels = {};
  for (const key of list) {
    const id = keyID(key);
    const name = keyName(key);
    if (!name) labels[id] = id;
    else if (names.get(name) === 1) labels[id] = name;
    else {
      const tag = keyTag(key, id);
      const shortID = id.slice(0, 8);
      const unique = tags.get(name + "\n" + tag) === 1 || tag === shortID;
      labels[id] = name + " (" + (unique ? tag : tag + ", " + shortID) + ")";
    }
  }
  return labels;
}

// authKeysNamed returns the keys whose name equals `name` after trimming,
// leaving out the key with id `exceptID` (the key being renamed).
export function authKeysNamed(keys, name, exceptID = "") {
  const wanted = String(name || "").trim();
  if (!wanted) return [];
  return (Array.isArray(keys) ? keys : []).filter(
    (key) => key && keyName(key) === wanted && keyID(key) !== exceptID,
  );
}
