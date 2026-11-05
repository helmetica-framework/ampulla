package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ampullav1 "github.com/helmetica-framework/ampulla/api/v1"
)

func TestRender(t *testing.T) {
	obj := map[string]any{
		"spec": map[string]any{
			"schedule":     "0 2 * * *",
			"maxRetention": int64(7),
			"s3": map[string]any{
				"bucket":          "${s3.bucket}",
				"endpoint":        "${s3.hostPort}",
				"endpointURL":     "${s3.url}",
				"region":          "${s3.region}",
				"destinationPath": "s3://${s3.bucket}/pg",
				"tls":             map[string]any{"enabled": "${s3.tls}"},
				"accessKey":       "${s3.accessKeyRef}",
				"secretKey":       "${s3.secretKeyRef}",
			},
		},
	}

	Render(obj, ampullav1.BackupPolicyStatus{
		Bucket:            "bucket-8f2a",
		Endpoint:          "https://objects.example.com:3900",
		Region:            "lpg",
		CredentialsSecret: "orders-backup-credentials",
	})

	assert.Equal(t, map[string]any{
		"spec": map[string]any{
			"schedule":     "0 2 * * *",
			"maxRetention": int64(7),
			"s3": map[string]any{
				"bucket":          "bucket-8f2a",
				"endpoint":        "objects.example.com:3900",
				"endpointURL":     "https://objects.example.com:3900",
				"region":          "lpg",
				"destinationPath": "s3://bucket-8f2a/pg",
				"tls":             map[string]any{"enabled": true},
				"accessKey":       map[string]any{"name": "orders-backup-credentials", "key": AccessKeyIDKey},
				"secretKey":       map[string]any{"name": "orders-backup-credentials", "key": AccessSecretKeyKey},
			},
		},
	}, obj)
}

func TestRender_PlainHTTPEndpoint(t *testing.T) {
	obj := map[string]any{"tls": "${s3.tls}", "note": "tls ${s3.tls}"}
	Render(obj, ampullav1.BackupPolicyStatus{Endpoint: "http://garage-s3.garage-system.svc:3900"})

	assert.Equal(t, false, obj["tls"])
	assert.Equal(t, "tls ${s3.tls}", obj["note"], "a typed value cannot be part of a string")
}

func TestHostOf_NoScheme(t *testing.T) {
	assert.Equal(t, "objects.example.com:3900", hostOf("objects.example.com:3900"))
}
