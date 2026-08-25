package controllers

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
)

func newTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(backupsv1.AddToScheme(scheme))
	return scheme
}

func policy(generation int64) *backupsv1.BackupPolicy {
	return &backupsv1.BackupPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sample",
			Namespace:  "default",
			Generation: generation,
		},
		Spec: backupsv1.BackupPolicySpec{},
	}
}

func policyKey() types.NamespacedName {
	return types.NamespacedName{Name: "sample", Namespace: "default"}
}

// newManager wires a BackupPolicyManager over a fake client seeded with objs. The status
// subresource must be registered or every status write is rejected.
func newManager(objs ...client.Object) (*BackupPolicyManager, client.Client) {
	scheme := newTestScheme()
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupsv1.BackupPolicy{}).
		WithObjects(objs...).
		Build()

	return &BackupPolicyManager{
		Client: c,
		Scheme: scheme,
		Log:    logr.Discard(),
	}, c
}

func TestReconcile_WritesPhase(t *testing.T) {
	r, c := newManager(policy(1))

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: policyKey()})
	require.NoError(t, err)

	got := &backupsv1.BackupPolicy{}
	require.NoError(t, c.Get(context.Background(), policyKey(), got))
	assert.Equal(t, backupsv1.BackupPolicyPhasePending, got.Status.Phase)
	assert.Equal(t, "nothing provisioned yet", got.Status.Message)
	assert.Equal(t, int64(1), got.Status.ObservedGeneration)
}

func TestReconcile_TracksGeneration(t *testing.T) {
	// A spec change bumps the generation, and the status has to say which one it reflects.
	r, c := newManager(policy(1))
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: policyKey()})
	require.NoError(t, err)

	current := &backupsv1.BackupPolicy{}
	require.NoError(t, c.Get(ctx, policyKey(), current))
	current.Generation = 2
	require.NoError(t, c.Update(ctx, current))

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: policyKey()})
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, policyKey(), current))
	assert.Equal(t, int64(2), current.Status.ObservedGeneration)
}

func TestReconcile_MissingPolicyIsNotAnError(t *testing.T) {
	r, _ := newManager()

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: policyKey()})
	assert.NoError(t, err, "a deleted policy is reconciled once more, and there is nothing to do")
}

func TestReconcile_SkipsPolicyBeingDeleted(t *testing.T) {
	deleting := policy(1)
	deleting.Finalizers = []string{"test.ampulla.helmetica.io/hold"}
	deleting.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

	r, c := newManager(deleting)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: policyKey()})
	require.NoError(t, err)

	got := &backupsv1.BackupPolicy{}
	require.NoError(t, c.Get(context.Background(), policyKey(), got))
	assert.Empty(t, got.Status.Phase, "nothing is written to a policy on its way out")
}
