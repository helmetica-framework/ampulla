package controllers

import (
	"testing"

	k8upv1 "github.com/k8up-io/k8up/v2/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	cosiv1alpha2 "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
	"github.com/helmetica-framework/ampulla/internal/backup"
)

func testPolicy() *backupsv1.BackupPolicy {
	return &backupsv1.BackupPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "orders", Name: "orders", UID: "policy-uid"},
	}
}

func testOwner() map[string]any {
	return map[string]any{"apiVersion": "backups.helmetica.io/v1", "kind": "BackupPolicy", "name": "orders"}
}

func testBucket() backup.Bucket {
	return backup.Bucket{
		ID:       "bucket-8f2a",
		Endpoint: "https://objects.lpg.example.com",
		Region:   "lpg",
	}
}

// decode reads an apply configuration back into the type it will be applied as, rejecting
// any field the type does not have.
//
// This is what keeps the string keys in objects.go honest: the API server prunes an
// unknown field silently, so a typo would cost a setting rather than an error, and here it
// fails the test instead.
func decode[T any](t *testing.T, obj *unstructured.Unstructured) *T {
	t.Helper()

	converter := runtime.NewTestUnstructuredConverterWithValidation(equality.Semantic)
	var typed T
	require.NoError(t, converter.FromUnstructuredWithValidation(obj.Object, &typed, true),
		"every field of the applied object must exist on %T", &typed)
	return &typed
}

func TestObjectMeta(t *testing.T) {
	meta := objectMeta(testPolicy(), "orders-backup")

	assert.Equal(t, "orders", meta.Namespace, "everything lives beside the policy")
	assert.Equal(t, "orders", meta.Labels[managedLabel],
		"the cache filters on the key, and the value names the policy the object belongs to")
}

func TestControllerRef(t *testing.T) {
	owner, err := controllerRef(testPolicy(), Scheme())
	require.NoError(t, err)

	assert.Equal(t, "backups.helmetica.io/v1", owner["apiVersion"])
	assert.Equal(t, "BackupPolicy", owner["kind"])
	assert.Equal(t, "orders", owner["name"])
	assert.Equal(t, "policy-uid", owner["uid"], "an owner reference without the UID is rejected")
	assert.Equal(t, true, owner["controller"])
}

func TestScheduleFor(t *testing.T) {
	names := backup.NamesFor("orders")
	cfg := backup.Config{
		Mode: backupsv1.ModeSchedule,
		Schedules: backupsv1.ScheduleSpec{
			Backup: "0 2 * * *",
			Prune:  "0 3 * * 0",
			Check:  "0 4 * * 0",
		},
		Retention: backupsv1.Retention{KeepDaily: 7, KeepMonthly: 6},
	}

	schedule := decode[k8upv1.Schedule](t, scheduleFor(testPolicy(), testOwner(), names, cfg, testBucket()))

	require.NotNil(t, schedule.Spec.Backend.S3)
	assert.Equal(t, "https://objects.lpg.example.com", schedule.Spec.Backend.S3.Endpoint)
	assert.Equal(t, "bucket-8f2a", schedule.Spec.Backend.S3.Bucket,
		"the bucket is the one COSI provisioned, not anything derived from the policy name")

	// k8up reads the key pair straight out of the Secret COSI wrote: v1alpha2 gives every
	// credential its own key, so nothing has to be copied into a second Secret.
	assert.Equal(t, names.CredentialsSecret, schedule.Spec.Backend.S3.AccessKeyIDSecretRef.Name)
	assert.Equal(t, "COSI_S3_ACCESS_KEY_ID", schedule.Spec.Backend.S3.AccessKeyIDSecretRef.Key)
	assert.Equal(t, names.CredentialsSecret, schedule.Spec.Backend.S3.SecretAccessKeySecretRef.Name)
	assert.Equal(t, "COSI_S3_ACCESS_SECRET_KEY", schedule.Spec.Backend.S3.SecretAccessKeySecretRef.Key)

	require.NotNil(t, schedule.Spec.Backend.RepoPasswordSecretRef)
	assert.Equal(t, names.RepositorySecret, schedule.Spec.Backend.RepoPasswordSecretRef.Name)

	require.NotNil(t, schedule.Spec.Backup)
	assert.EqualValues(t, "0 2 * * *", schedule.Spec.Backup.Schedule)
	assert.Empty(t, schedule.Spec.Backup.LabelSelectors,
		"without a selector k8up backs up every PVC in the namespace, which is the whole service")

	require.NotNil(t, schedule.Spec.Prune)
	assert.EqualValues(t, "0 3 * * 0", schedule.Spec.Prune.Schedule)
	assert.Equal(t, 7, schedule.Spec.Prune.Retention.KeepDaily)
	assert.Equal(t, 6, schedule.Spec.Prune.Retention.KeepMonthly)
	assert.Zero(t, schedule.Spec.Prune.Retention.KeepLast,
		"a zero would be applied as an explicit keep-none and owned as such")

	require.NotNil(t, schedule.Spec.Check)
	assert.EqualValues(t, "0 4 * * 0", schedule.Spec.Check.Schedule)
}

func TestScheduleFor_WithoutPruneAndCheck(t *testing.T) {
	cfg := backup.Config{
		Mode:      backupsv1.ModeSchedule,
		Schedules: backupsv1.ScheduleSpec{Backup: "0 2 * * *"},
	}

	schedule := decode[k8upv1.Schedule](t,
		scheduleFor(testPolicy(), testOwner(), backup.NamesFor("orders"), cfg, testBucket()))

	assert.Nil(t, schedule.Spec.Prune, "an empty prune schedule means no prune job, not an empty cron expression")
	assert.Nil(t, schedule.Spec.Check)
}

func TestBucketClaimAndAccessFor(t *testing.T) {
	names := backup.NamesFor("orders")
	cfg := backup.Config{BucketClassName: "garage", BucketAccessClassName: "garage"}

	claim := decode[cosiv1alpha2.BucketClaim](t, bucketClaimFor(testPolicy(), testOwner(), names, cfg))
	assert.Equal(t, "garage", claim.Spec.BucketClassName)
	assert.Equal(t, []cosiv1alpha2.ObjectProtocol{cosiv1alpha2.ObjectProtocolS3}, claim.Spec.Protocols)
	assert.Equal(t, "orders", claim.Labels[managedLabel])

	access := decode[cosiv1alpha2.BucketAccess](t, bucketAccessFor(testPolicy(), testOwner(), names, cfg))
	assert.Equal(t, "garage", access.Spec.BucketAccessClassName)
	assert.Equal(t, cosiv1alpha2.ObjectProtocolS3, access.Spec.Protocol)
	assert.Equal(t, []cosiv1alpha2.BucketClaimAccess{{
		BucketClaimName:  names.BucketClaim,
		AccessMode:       cosiv1alpha2.BucketAccessModeReadWrite,
		AccessSecretName: names.CredentialsSecret,
	}}, access.Spec.BucketClaims, "one policy, one bucket, one key pair")
}
