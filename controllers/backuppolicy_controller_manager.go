package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
)

// BackupPolicyManager reconciles BackupPolicy objects.
type BackupPolicyManager struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Log      logr.Logger
}

// phase is what a reconcile concluded about a policy.
type phase struct {
	Phase   backupsv1.BackupPolicyPhase
	Message string
	Status  backupsv1.BackupPolicyStatus
}

// +kubebuilder:rbac:groups=backups.helmetica.io,resources=backuppolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=backups.helmetica.io,resources=backuppolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile drives a BackupPolicy's status to reflect desiredPhase. There is nothing else
// to do yet: the placeholder owns no other cluster objects, so this is just a
// settle-and-write loop over the policy itself.
func (r *BackupPolicyManager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("backuppolicy", req.NamespacedName)

	policy := &backupsv1.BackupPolicy{}
	err := r.Get(ctx, req.NamespacedName, policy)
	if err != nil {
		if apierrors.IsNotFound(err) {
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

	status := phase.Status
	status.Phase = phase.Phase
	status.Message = phase.Message
	status.ObservedGeneration = policy.Generation

	if policy.Status != status {
		if policy.Status.Phase != phase.Phase {
			log.Info("policy phase changed", "from", policy.Status.Phase, "to", phase.Phase)
		}
		policy.Status = status
		if err := r.Status().Update(ctx, policy); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating policy status: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

// desiredPhase is the seam where ampulla's real logic goes: provisioning the bucket the
// backups are written to, and the schedule that fills it.
func (r *BackupPolicyManager) desiredPhase(_ context.Context, _ logr.Logger, _ *backupsv1.BackupPolicy) (phase, error) {
	return phase{
		Phase:   backupsv1.BackupPolicyPhasePending,
		Message: "nothing provisioned yet",
	}, nil
}

// SetupWithManager wires the controller: watch BackupPolicy. Nothing else is owned yet, so
// there are no additional watches.
func (r *BackupPolicyManager) SetupWithManager(name string, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&backupsv1.BackupPolicy{}).
		Complete(r)
}
