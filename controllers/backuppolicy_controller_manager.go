package controllers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	k8upv1 "github.com/k8up-io/k8up/v2/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	cosiv1alpha2 "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
	backupsacv1 "github.com/helmetica-framework/ampulla/applyconfiguration/api/v1"
	"github.com/helmetica-framework/ampulla/internal/backup"
)

const (
	// managedLabel marks every object ampulla writes, with the name of the policy it was
	// written for. The manager's cache is filtered on the key alone, so ampulla never holds
	// the cluster's Secrets in memory.
	managedLabel = "ampulla.helmetica.io/managed"

	protocolS3          = string(cosiv1alpha2.ObjectProtocolS3)
	accessModeReadWrite = string(cosiv1alpha2.BucketAccessModeReadWrite)

	// repoPasswordKey is the key of the restic repository password in the generated secret.
	repoPasswordKey = "password"

	// fieldOwner is the field manager of everything ampulla applies. Server-side apply
	// tracks ownership under this name, which is what lets a field the policy stopped
	// asking for be removed rather than left behind.
	fieldOwner = client.FieldOwner("ampulla")

	// resyncInterval re-reconciles a ready policy. Nothing watches the COSI credentials
	// secret - it is written by COSI itself and carries none of ampulla's labels, so it is
	// outside the filtered cache - and a rotated endpoint is picked up on this interval.
	resyncInterval = time.Hour

	// credentialsRetry is the window in which COSI writes the credentials secret after it
	// reports the access as ready. Nothing else wakes the controller for it.
	credentialsRetry = 10 * time.Second
)

// The API versions of the objects ampulla applies. They are strings because these are
// apply configurations rather than typed objects; see the note in objects.go. Taken from
// the Go types so a dependency bump that moves a version does not leave a stale literal.
var (
	cosiAPIVersion = cosiv1alpha2.GroupVersion.String()
	k8upAPIVersion = k8upv1.GroupVersion.String()
)

// BackupPolicyManager reconciles BackupPolicy objects.
type BackupPolicyManager struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Log      logr.Logger

	// APIReader reads the COSI credentials secret. That secret is created by COSI without
	// ampulla's label, so the label-filtered cache cannot serve it.
	APIReader client.Reader

	// Defaults are the cluster-wide backup settings a policy may override.
	Defaults backup.Defaults
}

// phase is what a reconcile concluded, plus how long to wait before looking again.
type phase struct {
	Phase   backupsv1.BackupPolicyPhase
	Message string
	Status  backupsv1.BackupPolicyStatus
	Requeue time.Duration
}

// +kubebuilder:rbac:groups=backups.helmetica.io,resources=backuppolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=backups.helmetica.io,resources=backuppolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=objectstorage.k8s.io,resources=bucketclaims;bucketaccesses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=k8up.io,resources=schedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

// Reconcile drives a BackupPolicy's status to reflect desiredPhase, which is where the
// bucket and the schedule behind it are provisioned.
func (r *BackupPolicyManager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("backuppolicy", req.NamespacedName)

	policy := &backupsv1.BackupPolicy{}
	err := r.Get(ctx, req.NamespacedName, policy)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Everything ampulla created for this policy is owned by it, so a deleted
			// policy cleans up after itself.
			log.V(1).Info("policy is gone, nothing to do")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !policy.GetDeletionTimestamp().IsZero() {
		log.V(1).Info("policy is being deleted, nothing to do")
		return ctrl.Result{}, nil
	}

	phase, err := r.desiredPhase(ctx, log, policy)
	if err != nil {
		return ctrl.Result{}, err
	}

	want := phase.Status
	want.Phase = phase.Phase
	want.Message = phase.Message
	want.ObservedGeneration = policy.Generation

	if policy.Status != want {
		if policy.Status.Phase != phase.Phase {
			log.Info("policy phase changed", "from", policy.Status.Phase, "to", phase.Phase)
		}
		// Apply sends only the fields set below, so a field this reconcile no longer sets -
		// the schedule, once a policy switches to BucketOnly - is dropped from the status
		// rather than left over from the last one.
		status := backupsacv1.BackupPolicy(policy.Name, policy.Namespace).
			WithStatus(statusFor(want))
		if err := r.Status().Apply(ctx, status, fieldOwner, client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying policy status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: phase.Requeue}, nil
}

// statusFor is the status as an apply configuration. Only the fields that carry something
// are set: an empty one would be applied as an explicit empty string and owned as such.
func statusFor(status backupsv1.BackupPolicyStatus) *backupsacv1.BackupPolicyStatusApplyConfiguration {
	ac := backupsacv1.BackupPolicyStatus().
		WithPhase(status.Phase).
		WithObservedGeneration(status.ObservedGeneration)

	if status.Message != "" {
		ac.WithMessage(status.Message)
	}
	if status.Bucket != "" {
		ac.WithBucket(status.Bucket)
	}
	if status.Endpoint != "" {
		ac.WithEndpoint(status.Endpoint)
	}
	if status.CredentialsSecret != "" {
		ac.WithCredentialsSecret(status.CredentialsSecret)
	}
	if status.Schedule != "" {
		ac.WithSchedule(status.Schedule)
	}
	return ac
}

// desiredPhase provisions what the policy asks for and reports how far it got. Each early
// return is a state COSI has to leave before there is anything more to do, and the watches
// on the claim and the access are what bring the controller back.
func (r *BackupPolicyManager) desiredPhase(ctx context.Context, log logr.Logger, policy *backupsv1.BackupPolicy) (phase, error) {
	cfg, err := backup.Resolve(policy.Spec, r.Defaults)
	if err != nil {
		// A rejected policy needs a spec change to become valid, and a spec change
		// reconciles again. Requeueing with backoff would only repeat the same failure.
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, "NotActionable", "Backup", "%s", err)
		return phase{Phase: backupsv1.BackupPolicyPhaseFailed, Message: err.Error()}, nil
	}

	names := backup.NamesFor(policy.Name)

	// One owner reference for every object below: they are all the policy's, and it is the
	// only thing that keeps them from outliving it.
	owner, err := controllerRef(policy, r.Scheme)
	if err != nil {
		return phase{}, err
	}

	claim := &cosiv1alpha2.BucketClaim{}
	if err := r.applyOwned(ctx, bucketClaimFor(policy, owner, names, cfg)); err != nil {
		return phase{}, fmt.Errorf("applying bucket claim: %w", err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: names.BucketClaim}, claim); err != nil {
		return phase{}, fmt.Errorf("reading back bucket claim: %w", err)
	}
	if !ptr.Deref(claim.Status.ReadyToUse, false) {
		log.V(1).Info("waiting for COSI to provision the bucket", "bucketClaim", claim.Name)
		return phase{
			Phase:   backupsv1.BackupPolicyPhasePending,
			Message: waitMessage("waiting for the bucket to be provisioned", claim.Status.Error),
		}, nil
	}

	access := &cosiv1alpha2.BucketAccess{}
	if err := r.applyOwned(ctx, bucketAccessFor(policy, owner, names, cfg)); err != nil {
		return phase{}, fmt.Errorf("applying bucket access: %w", err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: names.BucketAccess}, access); err != nil {
		return phase{}, fmt.Errorf("reading back bucket access: %w", err)
	}
	if !ptr.Deref(access.Status.ReadyToUse, false) {
		log.V(1).Info("waiting for COSI to grant access to the bucket", "bucketAccess", access.Name)
		return phase{
			Phase:   backupsv1.BackupPolicyPhasePending,
			Message: waitMessage("waiting for the bucket credentials", access.Status.Error),
			Status:  backupsv1.BackupPolicyStatus{Bucket: claim.Status.BoundBucketName},
		}, nil
	}

	bucket, err := r.bucket(ctx, policy.Namespace, names.CredentialsSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// COSI writes the secret just before it reports the access as ready, so this is
			// a race with our own cache-free read rather than a real problem.
			log.V(1).Info("credentials secret not written yet, retrying", "secret", names.CredentialsSecret)
			return phase{
				Phase:   backupsv1.BackupPolicyPhasePending,
				Message: "waiting for the bucket credentials",
				Requeue: credentialsRetry,
			}, nil
		}
		return phase{}, err
	}

	status := backupsv1.BackupPolicyStatus{
		Bucket:            bucket.ID,
		Endpoint:          bucket.Endpoint,
		CredentialsSecret: names.CredentialsSecret,
	}

	if cfg.Mode == backupsv1.ModeBucketOnly {
		// The service backs itself up into this bucket. Nothing else to create - and if the
		// policy used to run in ModeSchedule, the schedule has to go, or k8up would keep
		// backing up volumes the service is now handling itself.
		if err := r.deleteSchedule(ctx, log, policy.Namespace, names.Schedule); err != nil {
			return phase{}, err
		}
		return phase{
			Phase:   backupsv1.BackupPolicyPhaseReady,
			Message: fmt.Sprintf("bucket %s is ready for the service to back up to", bucket.ID),
			Status:  status,
			Requeue: resyncInterval,
		}, nil
	}

	if err := r.ensureRepositoryPassword(ctx, log, policy, names.RepositorySecret); err != nil {
		return phase{}, err
	}

	if err := r.applyOwned(ctx, scheduleFor(policy, owner, names, cfg, bucket)); err != nil {
		return phase{}, fmt.Errorf("applying schedule: %w", err)
	}

	status.Schedule = cfg.Schedules.Backup
	return phase{
		Phase:   backupsv1.BackupPolicyPhaseReady,
		Message: fmt.Sprintf("backing up to %s in %s", bucket.ID, bucket.Endpoint),
		Status:  status,
		Requeue: resyncInterval,
	}, nil
}

// waitMessage surfaces what COSI last said about a resource that is not ready, so a policy
// whose driver rejected the claim does not sit at "waiting" with the reason buried in
// another object's status.
func waitMessage(waiting string, cosiErr *cosiv1alpha2.TimestampedError) string {
	if cosiErr == nil || ptr.Deref(cosiErr.Message, "") == "" {
		return waiting
	}
	return fmt.Sprintf("%s: %s", waiting, *cosiErr.Message)
}

// deleteSchedule removes the k8up Schedule, which is what a switch to ModeBucketOnly has
// to do with the one it used to run.
func (r *BackupPolicyManager) deleteSchedule(ctx context.Context, log logr.Logger, namespace, name string) error {
	schedule := &k8upv1.Schedule{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := r.Delete(ctx, schedule); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting schedule %s/%s: %w", namespace, name, err)
	}
	log.Info("deleted schedule", "namespace", namespace, "name", name)
	return nil
}

// bucket reads the bucket out of the secret COSI populated for the BucketAccess.
func (r *BackupPolicyManager) bucket(ctx context.Context, namespace, name string) (backup.Bucket, error) {
	secret := &corev1.Secret{}
	// Read past the cache: this secret belongs to COSI and carries none of ampulla's
	// labels, so the filtered cache would report it as missing forever.
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		return backup.Bucket{}, err
	}

	bucket, err := backup.BucketFromSecret(secret.Data)
	if err != nil {
		return backup.Bucket{}, fmt.Errorf("reading secret %s/%s: %w", namespace, name, err)
	}
	return bucket, nil
}

// ensureRepositoryPassword creates the restic repository password once and then leaves it
// alone. Rewriting it would leave every existing snapshot in the bucket unreadable, so
// this is the one object here that is created rather than applied.
func (r *BackupPolicyManager) ensureRepositoryPassword(ctx context.Context, log logr.Logger, policy *backupsv1.BackupPolicy, name string) error {
	existing := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: policy.Namespace, Name: name}, existing)
	if err == nil {
		if len(existing.Data[repoPasswordKey]) == 0 {
			return fmt.Errorf("repository password secret %s/%s has no %q key", policy.Namespace, name, repoPasswordKey)
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading repository password secret: %w", err)
	}

	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return fmt.Errorf("generating a repository password: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: objectMeta(policy, name),
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{repoPasswordKey: base64.RawURLEncoding.EncodeToString(password)},
	}
	if err := controllerutil.SetControllerReference(policy, secret, r.Scheme); err != nil {
		return fmt.Errorf("owning the repository password secret: %w", err)
	}
	if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating the repository password secret: %w", err)
	}

	log.Info("generated a restic repository password", "namespace", policy.Namespace, "name", name)
	return nil
}

// applyOwned server-side-applies one of the objects built in objects.go.
//
// Apply sends only the fields the configuration sets, so a field the policy has stopped
// asking for - a prune schedule that was removed - is dropped rather than left behind, and
// two reconciles racing on the same object no longer collide the way a read-modify-write
// did. ForceOwnership takes over fields an earlier version of ampulla wrote under a
// different manager.
func (r *BackupPolicyManager) applyOwned(ctx context.Context, obj *unstructured.Unstructured) error {
	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj), fieldOwner, client.ForceOwnership)
}

// SetupWithManager wires the controller: watch BackupPolicy, plus everything it owns, so
// COSI reporting a bucket ready brings the policy straight back.
func (r *BackupPolicyManager) SetupWithManager(name string, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&backupsv1.BackupPolicy{}).
		Owns(&cosiv1alpha2.BucketClaim{}).
		Owns(&cosiv1alpha2.BucketAccess{}).
		Owns(&k8upv1.Schedule{}).
		Complete(r)
}
