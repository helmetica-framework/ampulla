package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestSchemeRegistration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))

	assert.True(t, scheme.Recognizes(GroupVersion.WithKind("BackupPolicy")))
	assert.True(t, scheme.Recognizes(GroupVersion.WithKind("BackupPolicyList")))
	assert.Equal(t, "backups.helmetica.io", GroupVersion.Group)
	assert.Equal(t, "v1", GroupVersion.Version)
}

func TestBackupPolicyDeepCopy(t *testing.T) {
	orig := &BackupPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sample",
			Namespace:  "default",
			Generation: 3,
			Labels:     map[string]string{"key": "value"},
		},
		Spec: BackupPolicySpec{
			Mode:            ModeBucketOnly,
			Schedule:        "0 2 * * *",
			BucketClassName: "backups",
			Retention:       Retention{KeepDaily: 7},
		},
		Status: BackupPolicyStatus{
			Phase:              BackupPolicyPhaseReady,
			ObservedGeneration: 3,
			Bucket:             "bucket-8f2a",
		},
	}

	copied := orig.DeepCopy()
	assert.Equal(t, orig, copied)

	copied.Labels["key"] = "changed"
	copied.Spec.Retention.KeepDaily = 14
	assert.Equal(t, "value", orig.Labels["key"], "the copy shares no maps with the original")
	assert.Equal(t, 7, orig.Spec.Retention.KeepDaily)
}

func TestRetentionIsZero(t *testing.T) {
	assert.True(t, Retention{}.IsZero())
	assert.False(t, Retention{KeepLast: 1}.IsZero())
	assert.False(t, Retention{KeepYearly: 1}.IsZero(), "every field counts, not just the first")
}
