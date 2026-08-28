package backup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNamesFor(t *testing.T) {
	names := NamesFor("mariadb")

	assert.Equal(t, Names{
		BucketClaim:       "mariadb-backup",
		BucketAccess:      "mariadb-backup",
		CredentialsSecret: "mariadb-backup-credentials",
		RepositorySecret:  "mariadb-backup-repository",
		Schedule:          "mariadb-backup",
	}, names)
}

func TestNamesFor_LongInstanceName(t *testing.T) {
	long := strings.Repeat("a", maxNameLength)
	// Two instances that only differ past the cut must not collide.
	other := long[:maxNameLength-1] + "b"

	for _, name := range []string{
		NamesFor(long).Schedule,
		NamesFor(long).RepositorySecret,
	} {
		assert.LessOrEqual(t, len(name), maxNameLength, "%q is too long to be an object name", name)
	}

	assert.NotEqual(t, NamesFor(long).Schedule, NamesFor(other).Schedule)
}
