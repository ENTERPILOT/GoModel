package config

import (
	"crypto/rand"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// onlyReferences reports whether value is made of secret references alone,
// optionally after an HTTP auth scheme ("Bearer ${env:TOKEN}").
func onlyReferences(value string) bool {
	if OnlySecretReferences(value) {
		return true
	}
	_, rest, ok := cutAuthScheme(value)
	return ok && OnlySecretReferences(rest)
}

// cutAuthScheme splits an Authorization value into its scheme and credential.
func cutAuthScheme(value string) (scheme, credential string, ok bool) {
	scheme, credential, found := strings.Cut(value, " ")
	switch strings.ToLower(scheme) {
	case "bearer", "basic", "token":
		return scheme, credential, found && credential != ""
	}
	return "", "", false
}

// referencePlaceholder stands in for a secret reference while a value is
// parsed: braces are not valid in a URL. It is random, so no literal text
// can pass for a reference.
var referencePlaceholder = "gomodelref" + strings.ToLower(rand.Text())

// literal reports whether s, with references replaced by
// referencePlaceholder, holds anything but references.
func literal(s string) bool {
	return strings.ReplaceAll(s, referencePlaceholder, "") != ""
}

// urlHasLiteralCredential reports whether a URL carries a password, a
// username without one, or a query parameter named like a credential, that is
// not made of secret references alone. A URL that does not parse is assumed to.
func urlHasLiteralCredential(value string) bool {
	credentials, parsed := urlCredentials(value)
	return !parsed || len(credentials) > 0
}

// urlCredentials returns the credentials a URL carries that are not made of
// secret references alone, with references in them replaced by
// referencePlaceholder. It reports false for a URL that does not parse.
func urlCredentials(value string) ([]string, bool) {
	if !strings.ContainsAny(value, "@?#") {
		return nil, true
	}
	u, err := url.Parse(replaceReferences(value, referencePlaceholder))
	if err != nil {
		return nil, false
	}
	var credentials []string
	// A username without a password is often a token ("TOKEN@host", or
	// "TOKEN:@host").
	if u.User != nil {
		credential := u.User.Username()
		if password, set := u.User.Password(); set && password != "" {
			credential = password
		}
		if literal(credential) {
			credentials = append(credentials, credential)
		}
	}
	// A fragment can carry a query too ("#access_token=...").
	fragment, _ := url.ParseQuery(u.Fragment)
	for _, query := range []url.Values{u.Query(), fragment} {
		for key, values := range query {
			if credentialKey(key) {
				credentials = append(credentials, slices.DeleteFunc(values, func(v string) bool { return !literal(v) })...)
			}
		}
	}
	return credentials, true
}

// assignment finds name=value pairs, as in a key=value connection string
// ("host=db user=gomodel password=...") or a query string.
var assignment = regexp.MustCompile(`(?:^|[\s;,&?])([A-Za-z][A-Za-z0-9_.\-]*)\s*=\s*('(?:[^'\\]|\\.)*'|[^\s;,&]*)`)

// jsonAssignment finds "name": value pairs, as in JSON text. The value is a
// string, or the start of any other JSON value.
var jsonAssignment = regexp.MustCompile(`("(?:[^"\\]|\\.)*")\s*:\s*("(?:[^"\\]|\\.)*"|[^\s,}\]]+|[\[{])`)

// assignsLiteralCredential reports whether value assigns a name that names a
// credential a value that is not made of secret references alone, as
// name=value or as JSON.
func assignsLiteralCredential(value string) bool {
	text := replaceReferences(value, referencePlaceholder)
	for _, match := range assignment.FindAllStringSubmatch(text, -1) {
		if credentialKey(match[1]) && literal(strings.Trim(match[2], "'")) {
			return true
		}
	}
	for _, match := range jsonAssignment.FindAllStringSubmatch(text, -1) {
		if credentialKey(jsonText(match[1])) && match[2] != "null" && literal(jsonText(match[2])) {
			return true
		}
	}
	return false
}

// jsonText decodes a JSON string, escapes included ("api\u005fkey"); other
// JSON text, such as a number, is returned as is.
func jsonText(raw string) string {
	var s string
	if json.Unmarshal([]byte(raw), &s) != nil {
		return raw
	}
	return s
}

// bearerCredential reports whether value is a bearer token
// ("Bearer abc123"): the scheme and one word that is not a reference.
func bearerCredential(value string) bool {
	scheme, credential, ok := cutAuthScheme(value)
	credential = strings.TrimLeft(credential, " \t")
	return ok && strings.EqualFold(scheme, "bearer") && !strings.ContainsAny(credential, " \t\n") &&
		literal(replaceReferences(credential, referencePlaceholder))
}

// argCredential reports how a command-line argument carries a credential:
// after a prefix naming one ("--api-key=", "GITHUB_TOKEN=",
// "Authorization: ", "--header=Authorization: "), whose length it returns,
// or in the next argument, after a flag naming one ("--token"). A credential
// named inside structured text (`{"apiKey":`) is followed by more of its
// syntax, so the whole text is the credential: the prefix is 0, or ends
// before the text (`--config=`).
func argCredential(arg string) (prefix int, next bool) {
	if i := strings.IndexAny(arg, "=:"); i > 0 {
		name := strings.TrimLeft(arg[:i], "-")
		if !credentialKey(name) {
			inner, _ := argCredential(arg[i+1:])
			switch {
			case inner < 0:
				return -1, false
			case strings.ContainsFunc(name, structural):
				// A credential later in structured text: the text starts here.
				return 0, false
			}
			return i + 1 + inner, false
		}
		if strings.ContainsFunc(name, structural) {
			return 0, false
		}
		end := i + 1
		for end < len(arg) && arg[end] == ' ' {
			end++
		}
		return end, false
	}
	if strings.HasPrefix(arg, "-") && credentialKey(strings.TrimLeft(arg, "-")) {
		return -1, true
	}
	return -1, false
}

// structural reports whether r is syntax of structured text, such as JSON,
// rather than part of a flag or header name.
func structural(r rune) bool {
	return strings.ContainsRune(`{}[]"',`, r)
}

// replaceReferences replaces every secret reference in value with with.
func replaceReferences(value, with string) string {
	var b strings.Builder
	for rest := value; ; {
		i := strings.Index(rest, "${")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		end := strings.IndexByte(rest[i:], '}')
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		if _, _, ok := parseSecretReference(rest[i+2 : i+end]); ok {
			b.WriteString(with)
		} else {
			b.WriteString(rest[i : i+end+1])
		}
		rest = rest[i+end+1:]
	}
}

// credentialWords are the words of a setting name that mark its value as a
// credential where no schema says so.
var credentialWords = map[string]bool{
	"key": true, "apikey": true, "token": true, "secret": true, "password": true,
	"passwd": true, "pass": true, "pwd": true, "pw": true, "credential": true, "credentials": true,
	"auth": true, "authorization": true, "bearer": true, "cookie": true, "dsn": true,
	"private": true, "jwt": true, "sig": true, "signature": true, "pat": true,
	"sslpassword": true,
}

// credentialKey reports whether a setting name names a credential: one of
// its words is in credentialWords. Words are split at every character but
// letters and digits, and where a lower-case letter is followed by an
// upper-case one; a trailing number is dropped ("token2").
func credentialKey(key string) bool {
	var word []rune
	credential := false
	flush := func() {
		if credentialWords[strings.TrimRightFunc(strings.ToLower(string(word)), unicode.IsDigit)] {
			credential = true
		}
		word = word[:0]
	}
	prevLower := false
	for _, r := range key {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			prevLower = false
			continue
		case unicode.IsUpper(r) && prevLower:
			flush()
		}
		word = append(word, r)
		prevLower = unicode.IsLower(r)
	}
	flush()
	return credential
}

// suggestedEnvName names the variable to set for a secret at path that no
// variable supplies: GOMODEL_ and the path in upper case.
func suggestedEnvName(path string) string {
	var b strings.Builder
	b.WriteString("GOMODEL")
	underscore := false
	for _, r := range strings.ToUpper(path) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			if !underscore {
				b.WriteByte('_')
				underscore = true
			}
			b.WriteRune(r)
			continue
		}
		underscore = false
	}
	return b.String()
}
