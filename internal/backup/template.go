package backup

import (
	"net/url"
	"strings"

	ampullav1 "github.com/helmetica-framework/ampulla/api/v1"
)

// Render replaces the placeholders in obj, in place, with the bucket a BackupPolicy reports
// in its status. They describe what a value is, not where COSI keeps it, so a template only
// has to know the shape of its own operator's resource.
func Render(obj map[string]any, status ampullav1.BackupPolicyStatus) {
	// Replaced anywhere in a string.
	replacer := strings.NewReplacer(
		"${s3.bucket}", status.Bucket,
		"${s3.url}", status.Endpoint,
		"${s3.hostPort}", hostOf(status.Endpoint),
		"${s3.region}", status.Region,
	)
	// Not strings, so they only stand for a whole value.
	typed := map[string]any{
		"${s3.tls}":          strings.HasPrefix(status.Endpoint, "https://"),
		"${s3.accessKeyRef}": secretKeyRef(status.CredentialsSecret, AccessKeyIDKey),
		"${s3.secretKeyRef}": secretKeyRef(status.CredentialsSecret, AccessSecretKeyKey),
	}
	replace(obj, replacer, typed)
}

func replace(value any, replacer *strings.Replacer, typed map[string]any) any {
	switch v := value.(type) {
	case string:
		if t, ok := typed[v]; ok {
			return t
		}
		return replacer.Replace(v)
	case map[string]any:
		for key, elem := range v {
			v[key] = replace(elem, replacer, typed)
		}
	case []any:
		for i, elem := range v {
			v[i] = replace(elem, replacer, typed)
		}
	}
	return value
}

// secretKeyRef is the `{name, key}` reference most operators take for a credential.
func secretKeyRef(name, key string) map[string]any {
	return map[string]any{"name": name, "key": key}
}

// hostOf strips the scheme off the endpoint URL COSI publishes. Most operators want
// `host:port` and a separate TLS flag.
func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	return u.Host
}
