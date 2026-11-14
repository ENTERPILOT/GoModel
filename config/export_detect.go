package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
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
	text := replaceReferences(value, referencePlaceholder)
	var credentials []string
	if password, ok := schemelessPassword(text); ok && literal(password) {
		credentials = append(credentials, password)
	}
	u, err := url.Parse(text)
	if err != nil {
		return credentials, false
	}
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
	params, _ := urlParameters(u)
	for _, query := range params {
		for key, values := range query {
			if !safeQueryParameters[strings.ToLower(key)] && credentialKey(key) {
				credentials = append(credentials, slices.DeleteFunc(values, func(v string) bool { return !literal(v) })...)
			}
		}
	}
	return credentials, true
}

// urlParameters returns the parameters of a URL's query and of its fragment,
// which can carry a query too ("#access_token=..."). It reports false when
// either does not parse, which drops the malformed parameters.
func urlParameters(u *url.URL) ([]url.Values, bool) {
	query, queryErr := url.ParseQuery(u.RawQuery)
	// Escaped, so an encoded delimiter ("%3B") stays inside its value.
	fragment, fragmentErr := url.ParseQuery(u.EscapedFragment())
	return []url.Values{query, fragment}, queryErr == nil && fragmentErr == nil
}

// portSuffix matches the port ending a host ("db:5432").
var portSuffix = regexp.MustCompile(`:\d+$`)

// schemelessPassword returns the password of a connection string that names
// user:password@address without a scheme ("app:pw@tcp(db:3306)/gomodel",
// "app:pw@db:5432"), which url.Parse reads as a scheme and no user
// information. The address must hold a protocol, a path, or a port, which
// tells it from an address such as "mailto:ops@example.com".
func schemelessPassword(value string) (string, bool) {
	head := value
	if end := strings.IndexAny(head, "?#"); end >= 0 {
		head = head[:end]
	}
	at := strings.LastIndex(head, "@")
	if at < 0 || strings.ContainsAny(value, " \t\n") {
		return "", false
	}
	user, password, found := strings.Cut(head[:at], ":")
	address := head[at+1:]
	switch {
	case !found || user == "" || password == "":
		return "", false
	case strings.HasPrefix(password, "//") || strings.Contains(user, "/"):
		// A URL with a scheme, which url.Parse reads, or a path.
		return "", false
	case !strings.ContainsAny(address, "/(") && !portSuffix.MatchString(address):
		return "", false
	}
	return password, true
}

// safeQueryParameters are the query parameters of the URLs settings hold that
// are known to carry no credential, in lower case: API versions and the
// connection options of the PostgreSQL, MongoDB, and Redis drivers.
var safeQueryParameters = map[string]bool{
	"api-version": true, "api_version": true, "version": true, "v": true,
	// PostgreSQL
	"sslmode": true, "connect_timeout": true, "application_name": true, "search_path": true,
	"target_session_attrs": true, "pool_max_conns": true, "pool_min_conns": true,
	"pool_max_conn_lifetime": true, "pool_max_conn_idle_time": true,
	// MongoDB
	"replicaset": true, "directconnection": true, "authsource": true, "authmechanism": true,
	"retrywrites": true, "retryreads": true, "w": true, "tls": true, "ssl": true, "appname": true,
	"maxpoolsize": true, "minpoolsize": true, "connecttimeoutms": true, "sockettimeoutms": true,
	"serverselectiontimeoutms": true, "readpreference": true, "compressors": true,
	// Redis
	"db": true, "protocol": true, "dial_timeout": true, "read_timeout": true, "write_timeout": true,
	"pool_size": true, "max_retries": true,
}

// urlSettingHasSecret reports whether the value of a URL setting holds a
// secret: a credential, as urlHasLiteralCredential reports, or a query or
// fragment parameter that is not in safeQueryParameters and not made of
// secret references alone. A parameter can carry a credential under any name
// ("?code=...", "?sv=...&se=..."), so in a URL setting only known ones are
// written out. A URL whose parameters do not parse is assumed to hold one.
func urlSettingHasSecret(value string) bool {
	if urlHasLiteralCredential(value) {
		return true
	}
	if !strings.ContainsAny(value, "?#") {
		return false
	}
	u, err := url.Parse(replaceReferences(value, referencePlaceholder))
	if err != nil {
		return true
	}
	params, parsed := urlParameters(u)
	if !parsed {
		return true
	}
	for _, query := range params {
		for key, values := range query {
			if !safeQueryParameters[strings.ToLower(key)] && slices.ContainsFunc(values, literal) {
				return true
			}
		}
	}
	return false
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

// authCredential reports whether value is an Authorization credential
// ("Bearer abc123", "Basic dXNlcjpwYXNz"): the scheme and one word that is
// not a reference. Basic is an ordinary word too ("Basic plan"), so the word
// after it must be base64 of user:password.
func authCredential(value string) bool {
	scheme, credential, ok := cutAuthScheme(value)
	credential = strings.TrimLeft(credential, " \t")
	if !ok || strings.ContainsAny(credential, " \t\n") || !literal(replaceReferences(credential, referencePlaceholder)) {
		return false
	}
	switch strings.ToLower(scheme) {
	case "bearer":
		return true
	case "basic":
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
			if decoded, err := encoding.DecodeString(credential); err == nil && bytes.IndexByte(decoded, ':') >= 0 {
				return true
			}
		}
	}
	return false
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

// credentialStems mark a word as naming a credential wherever they appear in
// it, so a name written without separators is caught too ("privatekey",
// "clientsecret", "passphrase", "apitokens"). The shorter credentialWords
// ("pass", "pat", "sig", "auth") match whole words only: they are part of too
// many other words ("path", "design", "author").
var credentialStems = []string{
	"key", "token", "secret", "password", "passwd", "passphrase", "credential",
	"bearer", "cookie", "signature", "license", "licence",
}

// plainWords hold a credential stem without naming a credential.
var plainWords = map[string]bool{
	"keyword": true, "keywords": true, "keyboard": true, "keyspace": true, "keycloak": true,
	"keyvault": true, "monkey": true, "turkey": true, "donkey": true, "hockey": true,
	"tokenizer": true, "tokenizers": true, "tokenize": true, "tokenized": true,
	"tokenization": true, "detokenize": true, "maxtokens": true,
}

// modelTokenWords next to "token" or "tokens" mark it as about the tokens a
// model reads and writes rather than a credential ("max_tokens",
// "token_limit", "skip_special_tokens", "stop_token_ids").
var modelTokenWords = map[string]bool{
	"max": true, "min": true, "limit": true, "limits": true, "count": true, "budget": true,
	"total": true, "num": true, "per": true, "usage": true, "prompt": true, "completion": true,
	"input": true, "output": true, "reasoning": true, "thinking": true,
	"context": true, "special": true, "ids": true,
}

// modelTokens reports whether words[i], "token" or "tokens", is about model
// tokens: a word next to it is in modelTokenWords. Only a neighbor counts, so
// "new_access_token" still names a credential; "new" is skipped on the left
// ("max_new_tokens"). "cached" counts before the plural only: "cached_tokens"
// is a count, "cached_token" a credential.
func modelTokens(words []string, i int) bool {
	if words[i] != "token" && words[i] != "tokens" {
		return false
	}
	left := i - 1
	if left > 0 && words[left] == "new" {
		left--
	}
	if left >= 0 && words[left] == "cached" {
		if words[i] == "tokens" {
			return true
		}
		left = -1
	}
	return left >= 0 && modelTokenWords[words[left]] || i+1 < len(words) && modelTokenWords[words[i+1]]
}

// credentialKey reports whether a setting name names a credential: one of
// its words is in credentialWords or holds one of credentialStems, and is
// neither in plainWords nor about model tokens.
func credentialKey(key string) bool {
	words := nameWords(key)
	for i, word := range words {
		switch {
		case plainWords[word], modelTokens(words, i):
		case credentialWords[word]:
			return true
		case slices.ContainsFunc(credentialStems, func(stem string) bool { return strings.Contains(word, stem) }):
			return true
		}
	}
	return false
}

// nameWords returns the words of a setting name in lower case. Words are
// split at every character but letters and digits, and where a lower-case
// letter is followed by an upper-case one; a trailing number is dropped
// ("token2").
func nameWords(key string) []string {
	var words []string
	var word []rune
	flush := func() {
		if trimmed := strings.TrimRightFunc(strings.ToLower(string(word)), unicode.IsDigit); trimmed != "" {
			words = append(words, trimmed)
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
	return words
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
