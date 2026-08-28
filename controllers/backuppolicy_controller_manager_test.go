package controllers_test

import (
	"context"
	"testing"
	"time"

	k8upv1 "github.com/k8up-io/k8up/v2/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	cosiv1alpha2 "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
	"github.com/helmetica-framework/ampulla/controllers"
	"github.com/helmetica-framework/ampulla/internal/backup"
	"github.com/helmetica-framework/ampulla/testutil"
)

const (
	eventually = 20 * time.Second
	tick       = 100 * time.Millisecond
)

var testDefaults = backup.Defaults{
	BucketClassName:       "test-class",
	BucketAccessClassName: "test-class",
	Schedules: backupsv1.ScheduleSpec{
		Backup: "0 2 * * *",
		Prune:  "0 3 * * 0",
		Check:  "0 4 * * 0",
	},
}

// TestBackupPolicy walks a policy through the whole lifecycle: COSI provisioning the
// bucket and the credentials, the Schedule appearing, and the policy going away again.
func TestBackupPolicy(t *testing.T) {
	c := setup(t, testDefaults)
	ctx := t.Context()

	ns := testutil.TmpNamespace(t, c)
	policy := newPolicy(ns, "orders", backupsv1.BackupPolicySpec{})
	require.NoError(t, c.Create(ctx, policy))

	names := backup.NamesFor("orders")
	claim := &cosiv1alpha2.BucketClaim{}
	claimKey := client.ObjectKey{Namespace: ns, Name: names.BucketClaim}
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		assert.NoError(t, c.Get(ctx, claimKey, claim))
	}, eventually, tick, "the BucketClaim is created next to the policy")
	assert.Equal(t, "test-class", claim.Spec.BucketClassName)
	require.Len(t, claim.OwnerReferences, 1, "the policy owns it, so deleting the policy cleans up")
	assert.Equal(t, "BackupPolicy", claim.OwnerReferences[0].Kind)

	// From here on the test plays the part of COSI: envtest runs no COSI controller.
	provisionBucket(t, c, claim, "bucket-8f2a")

	access := &cosiv1alpha2.BucketAccess{}
	accessKey := client.ObjectKey{Namespace: ns, Name: names.BucketAccess}
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		assert.NoError(t, c.Get(ctx, accessKey, access))
	}, eventually, tick, "a ready bucket is followed by a request for credentials")
	assert.Equal(t, names.CredentialsSecret, access.Spec.BucketClaims[0].AccessSecretName)

	// No Schedule may exist yet: it would point at credentials that do not exist.
	require.Error(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.Schedule}, &k8upv1.Schedule{}))

	grantAccess(t, c, access, names.CredentialsSecret, ns)

	schedule := &k8upv1.Schedule{}
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		assert.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.Schedule}, schedule))
	}, eventually, tick, "the Schedule appears once the credentials are there")

	require.NotNil(t, schedule.Spec.Backend.S3)
	assert.Equal(t, "https://objects.example.com", schedule.Spec.Backend.S3.Endpoint)
	assert.Equal(t, "bucket-8f2a", schedule.Spec.Backend.S3.Bucket)
	assert.EqualValues(t, "0 2 * * *", schedule.Spec.Backup.Schedule)
	assert.Equal(t, backup.DefaultRetention.KeepDaily, schedule.Spec.Prune.Retention.KeepDaily)
	// k8up reads the key pair out of the Secret COSI wrote; ampulla copies nothing.
	assert.Equal(t, names.CredentialsSecret, schedule.Spec.Backend.S3.AccessKeyIDSecretRef.Name)

	var repoSecret corev1.Secret
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.RepositorySecret}, &repoSecret))
	password := repoSecret.Data["password"]
	assert.NotEmpty(t, password, "restic needs a repository password")

	require.EventuallyWithT(t, func(t *assert.CollectT) {
		current := &backupsv1.BackupPolicy{}
		assert.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(policy), current))
		assert.Equal(t, backupsv1.BackupPolicyPhaseReady, current.Status.Phase)
		assert.Equal(t, current.Generation, current.Status.ObservedGeneration)
		assert.Equal(t, "bucket-8f2a", current.Status.Bucket)
		assert.Equal(t, names.CredentialsSecret, current.Status.CredentialsSecret)
		assert.Equal(t, "0 2 * * *", current.Status.Schedule)
	}, eventually, tick, "the policy reports where its backups go")

	t.Run("the repository password survives a reconcile", func(t *testing.T) {
		// Rewriting it would leave every snapshot already in the bucket unreadable.
		touch(t, c, policy)

		require.Never(t, func() bool {
			var secret corev1.Secret
			require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.RepositorySecret}, &secret))
			return string(secret.Data["password"]) != string(password)
		}, 3*time.Second, tick)
	})

	t.Run("switching to BucketOnly removes the Schedule", func(t *testing.T) {
		setSpec(t, c, policy, backupsv1.BackupPolicySpec{Mode: backupsv1.ModeBucketOnly})

		require.EventuallyWithT(t, func(t *assert.CollectT) {
			err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.Schedule}, &k8upv1.Schedule{})
			assert.True(t, apierrors.IsNotFound(err), "the Schedule is deleted: %v", err)
		}, eventually, tick, "k8up must not keep backing up volumes the service now handles itself")

		require.EventuallyWithT(t, func(t *assert.CollectT) {
			current := &backupsv1.BackupPolicy{}
			assert.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(policy), current))
			assert.Equal(t, backupsv1.BackupPolicyPhaseReady, current.Status.Phase)
			assert.Empty(t, current.Status.Schedule)
			assert.Equal(t, names.CredentialsSecret, current.Status.CredentialsSecret,
				"the coordinates are the deliverable in this mode")
		}, eventually, tick)
	})
}

// TestBackupPolicy_NotActionable covers the case that must never fail silently: backups
// asked for, but no bucket class anywhere to provision them from.
func TestBackupPolicy_NotActionable(t *testing.T) {
	c := setup(t, backup.Defaults{Schedules: backupsv1.ScheduleSpec{Backup: "0 2 * * *"}})
	ctx := t.Context()

	ns := testutil.TmpNamespace(t, c)
	policy := newPolicy(ns, "unconfigured", backupsv1.BackupPolicySpec{})
	require.NoError(t, c.Create(ctx, policy))

	require.EventuallyWithT(t, func(t *assert.CollectT) {
		current := &backupsv1.BackupPolicy{}
		assert.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(policy), current))
		assert.Equal(t, backupsv1.BackupPolicyPhaseFailed, current.Status.Phase)
		assert.Contains(t, current.Status.Message, "no BucketClass")
	}, eventually, tick, "the policy says why it is not being backed up")

	names := backup.NamesFor("unconfigured")
	assert.True(t, apierrors.IsNotFound(
		c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.BucketClaim}, &cosiv1alpha2.BucketClaim{})),
		"a rejected policy provisions nothing")
}

// setup starts an API server and runs ampulla's manager against it.
func setup(t *testing.T, defaults backup.Defaults) client.Client {
	t.Helper()

	scheme, cfg := testutil.SetupEnvtestEnv(t)

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Cache:                  controllers.CacheOptions(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	bpm := controllers.BackupPolicyManager{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorder("backuppolicy-controller"),
		Log:       mgr.GetLogger().WithName("backuppolicy-controller"),
		APIReader: mgr.GetAPIReader(),
		Defaults:  defaults,
	}
	// Controller names are globally unique within the process, and every test starts its
	// own manager.
	require.NoError(t, bpm.SetupWithManager("backuppolicy-"+t.Name(), mgr))

	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		assert.NoError(t, mgr.Start(ctx))
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))

	return c
}

func newPolicy(namespace, name string, spec backupsv1.BackupPolicySpec) *backupsv1.BackupPolicy {
	return &backupsv1.BackupPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       spec,
	}
}

func setSpec(t *testing.T, c client.Client, policy *backupsv1.BackupPolicy, spec backupsv1.BackupPolicySpec) {
	t.Helper()

	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &backupsv1.BackupPolicy{}
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(policy), current); err != nil {
			return err
		}
		current.Spec = spec
		return c.Update(t.Context(), current)
	}))
}

// touch forces a reconcile without changing the policy.
func touch(t *testing.T, c client.Client, policy *backupsv1.BackupPolicy) {
	t.Helper()

	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &backupsv1.BackupPolicy{}
		if err := c.Get(t.Context(), client.ObjectKeyFromObject(policy), current); err != nil {
			return err
		}
		annotations := current.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["test.ampulla.helmetica.io/touched"] = time.Now().String()
		current.SetAnnotations(annotations)
		return c.Update(t.Context(), current)
	}))
}

// provisionBucket is what the COSI controller does once its driver created the bucket.
func provisionBucket(t *testing.T, c client.Client, claim *cosiv1alpha2.BucketClaim, bucketName string) {
	t.Helper()

	claim.Status.BoundBucketName = bucketName
	claim.Status.Protocols = []cosiv1alpha2.ObjectProtocol{cosiv1alpha2.ObjectProtocolS3}
	claim.Status.ReadyToUse = ptr.To(true)
	require.NoError(t, c.Status().Update(t.Context(), claim))
}

// grantAccess is what COSI does once its driver minted a key pair: it writes the Secret
// and only then reports the access as ready.
func grantAccess(t *testing.T, c client.Client, access *cosiv1alpha2.BucketAccess, secretName, namespace string) {
	t.Helper()

	require.NoError(t, c.Create(t.Context(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: secretName},
		StringData: map[string]string{
			"COSI_PROTOCOL":             string(cosiv1alpha2.ObjectProtocolS3),
			"COSI_S3_BUCKET_ID":         "bucket-8f2a",
			"COSI_S3_ENDPOINT":          "https://objects.example.com",
			"COSI_S3_REGION":            "garage",
			"COSI_S3_ADDRESSING_STYLE":  "path",
			"COSI_S3_ACCESS_KEY_ID":     "AKIA",
			"COSI_S3_ACCESS_SECRET_KEY": "s3cret",
		},
	}))

	access.Status.AccountID = "AKIA"
	access.Status.DriverName = "test.ampulla.helmetica.io"
	access.Status.AuthenticationType = cosiv1alpha2.BucketAccessAuthenticationTypeKey
	access.Status.AccessedBuckets = []cosiv1alpha2.AccessedBucket{{
		BucketName:      "bucket-8f2a",
		BucketID:        "bucket-8f2a",
		BucketClaimName: access.Spec.BucketClaims[0].BucketClaimName,
	}}
	access.Status.ReadyToUse = ptr.To(true)
	require.NoError(t, c.Status().Update(t.Context(), access))
}

// TestBackupPolicy_RemovingASchedule covers what a policy's owner sees when they take a
// schedule back out: the job it created has to go with it.
//
// The controller defaults here carry no prune or check schedule, so an empty field on the
// policy really does mean "no such job" - with a default configured it would mean "take
// the cluster's", which is the other half of the same behaviour.
func TestBackupPolicy_RemovingASchedule(t *testing.T) {
	defaults := backup.Defaults{
		BucketClassName:       "test-class",
		BucketAccessClassName: "test-class",
		Schedules:             backupsv1.ScheduleSpec{Backup: "0 2 * * *"},
	}
	c := setup(t, defaults)
	ctx := t.Context()

	ns := testutil.TmpNamespace(t, c)
	policy := newPolicy(ns, "orders", backupsv1.BackupPolicySpec{
		Schedule: backupsv1.ScheduleSpec{Prune: "0 3 * * 0", Check: "0 4 * * 0"},
	})
	require.NoError(t, c.Create(ctx, policy))

	names := backup.NamesFor("orders")
	readyBucket(t, c, ns, names)

	schedule := &k8upv1.Schedule{}
	scheduleKey := client.ObjectKey{Namespace: ns, Name: names.Schedule}
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		if !assert.NoError(t, c.Get(ctx, scheduleKey, schedule)) {
			return
		}
		assert.NotNil(t, schedule.Spec.Prune)
		assert.NotNil(t, schedule.Spec.Check)
	}, eventually, tick, "both jobs are scheduled while the policy asks for them")

	setSpec(t, c, policy, backupsv1.BackupPolicySpec{})

	require.EventuallyWithT(t, func(t *assert.CollectT) {
		if !assert.NoError(t, c.Get(ctx, scheduleKey, schedule)) {
			return
		}
		assert.Nil(t, schedule.Spec.Prune, "the prune job goes with the schedule that asked for it")
		assert.Nil(t, schedule.Spec.Check, "and so does the check")
	}, eventually, tick)

	assert.NotNil(t, schedule.Spec.Backup, "the backup itself still runs, on the controller's default")
	assert.EqualValues(t, "0 2 * * *", schedule.Spec.Backup.Schedule)
}

// readyBucket plays COSI's part: it provisions the claim's bucket, grants the access and
// writes the credentials secret, leaving the policy free to reach its Schedule.
func readyBucket(t *testing.T, c client.Client, ns string, names backup.Names) {
	t.Helper()
	ctx := t.Context()

	claim := &cosiv1alpha2.BucketClaim{}
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		assert.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.BucketClaim}, claim))
	}, eventually, tick)
	provisionBucket(t, c, claim, "bucket-8f2a")

	access := &cosiv1alpha2.BucketAccess{}
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		assert.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.BucketAccess}, access))
	}, eventually, tick)
	grantAccess(t, c, access, names.CredentialsSecret, ns)
}
