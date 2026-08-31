package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secret(overrides map[string]string) map[string][]byte {
	data := map[string]string{
		ProtocolKey:        "S3",
		BucketIDKey:        "bucket-8f2a",
		EndpointKey:        "https://objects.example.com",
		RegionKey:          "garage",
		AddressingStyleKey: "path",
		AccessKeyIDKey:     "AKIA",
		AccessSecretKeyKey: "s3cret",
	}
	for k, v := range overrides {
		if v == "" {
			delete(data, k)
			continue
		}
		data[k] = v
	}

	out := make(map[string][]byte, len(data))
	for k, v := range data {
		out[k] = []byte(v)
	}
	return out
}

func TestBucketFromSecret(t *testing.T) {
	bucket, err := BucketFromSecret(secret(nil))
	require.NoError(t, err)

	assert.Equal(t, Bucket{
		ID:       "bucket-8f2a",
		Endpoint: "https://objects.example.com",
		Region:   "garage",
	}, bucket)
}

func TestBucketFromSecret_WrongProtocol(t *testing.T) {
	_, err := BucketFromSecret(secret(map[string]string{ProtocolKey: "Azure"}))
	require.ErrorContains(t, err, "ampulla only backs up to S3")

	_, err = BucketFromSecret(nil)
	require.ErrorContains(t, err, "ampulla only backs up to S3", "an empty secret is not an S3 bucket either")
}

func TestBucketFromSecret_Incomplete(t *testing.T) {
	// A secret without credentials produces backup jobs that fail on every run with an
	// authentication error. Catching it here turns that into one message on the policy.
	_, err := BucketFromSecret(secret(map[string]string{AccessSecretKeyKey: ""}))
	require.ErrorContains(t, err, "no COSI_S3_ACCESS_SECRET_KEY")

	_, err = BucketFromSecret(secret(map[string]string{EndpointKey: "", BucketIDKey: ""}))
	require.ErrorContains(t, err, "no COSI_S3_ENDPOINT")
	require.ErrorContains(t, err, "no COSI_S3_BUCKET_ID")
}

func TestBucketFromSecret_RegionIsOptional(t *testing.T) {
	// k8up's S3 backend has no region field, so a driver that omits it is still usable.
	bucket, err := BucketFromSecret(secret(map[string]string{RegionKey: ""}))
	require.NoError(t, err)
	assert.Empty(t, bucket.Region)
	assert.Equal(t, "bucket-8f2a", bucket.ID)
}
