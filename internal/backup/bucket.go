package backup

import (
	"errors"
	"fmt"

	cosiv1alpha2 "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"
)

// The keys COSI writes into a BucketAccess's Secret. Each piece of bucket info gets its
// own key, which is what lets k8up's backend reference the Secret directly instead of
// ampulla copying credentials into a shape k8up can read.
const (
	ProtocolKey        = string(cosiv1alpha2.BucketInfoVar_Protocol)
	BucketIDKey        = string(cosiv1alpha2.BucketInfoVar_S3_BucketId)
	EndpointKey        = string(cosiv1alpha2.BucketInfoVar_S3_Endpoint)
	RegionKey          = string(cosiv1alpha2.BucketInfoVar_S3_Region)
	AddressingStyleKey = string(cosiv1alpha2.BucketInfoVar_S3_AddressingStyle)

	AccessKeyIDKey     = string(cosiv1alpha2.CredentialVar_S3_AccessKeyId)
	AccessSecretKeyKey = string(cosiv1alpha2.CredentialVar_S3_AccessSecretKey)
)

// Bucket is what ampulla needs out of that Secret to point k8up at the bucket. The
// credentials are deliberately not in here: they go from the Secret to the backup job by
// reference, without ever passing through this controller.
type Bucket struct {
	// ID is the bucket as clients address it, which is k8up's `bucket`.
	ID string
	// Endpoint is the S3 endpoint the bucket lives behind.
	Endpoint string
	// Region is reported by the driver. k8up's S3 backend has no field for it; it is kept
	// for the instance's status and for error messages.
	Region string
}

// BucketFromSecret reads the bucket out of the Secret COSI populated for a BucketAccess.
//
// The credential keys are checked but not returned: a Secret missing them produces backup
// jobs that fail on every run with an authentication error, and catching that here turns
// it into one clear message on the policy instead.
func BucketFromSecret(data map[string][]byte) (Bucket, error) {
	// k8up speaks several protocols; ampulla only wires up S3, which is the one every
	// driver in the framework provisions.
	if protocol := string(data[ProtocolKey]); protocol != string(cosiv1alpha2.ObjectProtocolS3) {
		return Bucket{}, fmt.Errorf("bucket speaks %q, but ampulla only backs up to S3", protocol)
	}

	var missing []error
	for _, key := range []string{BucketIDKey, EndpointKey, AccessKeyIDKey, AccessSecretKeyKey} {
		if len(data[key]) == 0 {
			missing = append(missing, fmt.Errorf("no %s", key))
		}
	}
	if err := errors.Join(missing...); err != nil {
		return Bucket{}, fmt.Errorf("incomplete bucket credentials: %w", err)
	}

	return Bucket{
		ID:       string(data[BucketIDKey]),
		Endpoint: string(data[EndpointKey]),
		Region:   string(data[RegionKey]),
	}, nil
}
