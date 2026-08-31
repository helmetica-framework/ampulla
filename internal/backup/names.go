package backup

import (
	"crypto/sha256"
	"fmt"
)

// maxNameLength is the limit for a DNS subdomain name, which is what all the objects
// ampulla creates are named by.
const maxNameLength = 253

// Names are the names of the objects ampulla creates for one BackupPolicy. They live in
// the policy's namespace beside the service, so the policy's name is what keeps them apart
// from anything the chart itself deploys.
type Names struct {
	// BucketClaim asks COSI for the bucket the backups are written to.
	BucketClaim string
	// BucketAccess asks COSI for a key pair on that bucket.
	BucketAccess string
	// CredentialsSecret is written by COSI and holds the bucket info and the key pair.
	CredentialsSecret string
	// RepositorySecret holds the restic repository password.
	RepositorySecret string
	// Schedule is the k8up Schedule taking the backups.
	Schedule string
}

// NamesFor derives the object names for a policy.
func NamesFor(policy string) Names {
	return Names{
		BucketClaim:       suffixed(policy, "-backup"),
		BucketAccess:      suffixed(policy, "-backup"),
		CredentialsSecret: suffixed(policy, "-backup-credentials"),
		RepositorySecret:  suffixed(policy, "-backup-repository"),
		Schedule:          suffixed(policy, "-backup"),
	}
}

// suffixed appends suffix to name, shortening name if the result would be too long to be
// a Kubernetes object name. Two instances whose names only differ past the cut still get
// different object names, because the hash is taken over the full name.
func suffixed(name, suffix string) string {
	if len(name)+len(suffix) <= maxNameLength {
		return name + suffix
	}

	const hashLen = 9 // "-" + 8 hex characters
	keep := maxNameLength - len(suffix) - hashLen
	hash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%s-%x%s", name[:keep], hash[:4], suffix)
}
