package controllers_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ampullav1 "github.com/helmetica-framework/ampulla/api/v1"
	"github.com/helmetica-framework/ampulla/internal/backup"
	"github.com/helmetica-framework/ampulla/testutil"
)

const templateServiceAccount = "instance-admin"

// TestBackupTemplate renders templates with a BucketOnly policy's bucket. ConfigMaps stand
// in for the operators' backup resources, whose CRDs envtest does not have.
func TestBackupTemplate(t *testing.T) {
	c := setup(t, testDefaults)
	ctx := t.Context()

	ns := testutil.TmpNamespace(t, c)
	grantTemplateAccess(t, c, ns)
	created := newTemplate(t, ns, "created", "orders", map[string]any{
		"bucket":   "${s3.bucket}",
		"endpoint": "${s3.hostPort}",
		"region":   "${s3.region}",
	})
	require.NoError(t, c.Create(ctx, created))

	require.EventuallyWithT(t, func(t *assert.CollectT) {
		current := &ampullav1.BackupTemplate{}
		assert.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(created), current))
		assert.Equal(t, ampullav1.BackupTemplatePhasePending, current.Status.Phase)
		assert.Contains(t, current.Status.Message, "waiting for BackupPolicy orders")
	}, eventually, tick, "a template waits for its policy")

	require.NoError(t, c.Create(ctx, newPolicy(ns, "orders", ampullav1.BackupPolicySpec{Mode: ampullav1.ModeBucketOnly})))
	names := backup.NamesFor("orders")
	readyBucket(t, c, ns, names)

	cm := &corev1.ConfigMap{}
	require.EventuallyWithT(t, func(t *assert.CollectT) {
		assert.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "created"}, cm))
	}, eventually, tick, "the policy becoming ready renders the template")
	assert.Equal(t, map[string]string{
		"bucket":   "bucket-8f2a",
		"endpoint": "objects.example.com",
		"region":   "garage",
	}, cm.Data)
	require.Len(t, cm.OwnerReferences, 1, "the object goes with its template")
	assert.Equal(t, "BackupTemplate", cm.OwnerReferences[0].Kind)
}

// TestBackupTemplate_Forbidden covers a template asking for an object its namespace's
// ServiceAccount may not write: ampulla must not write it with its own rights instead.
func TestBackupTemplate_Forbidden(t *testing.T) {
	c := setup(t, testDefaults)
	ctx := t.Context()

	ns := testutil.TmpNamespace(t, c)
	require.NoError(t, c.Create(ctx, newPolicy(ns, "orders", ampullav1.BackupPolicySpec{Mode: ampullav1.ModeBucketOnly})))
	readyBucket(t, c, ns, backup.NamesFor("orders"))

	tmpl := newTemplate(t, ns, "created", "orders", map[string]any{"bucket": "${s3.bucket}"})
	require.NoError(t, c.Create(ctx, tmpl))

	require.EventuallyWithT(t, func(t *assert.CollectT) {
		current := &ampullav1.BackupTemplate{}
		assert.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tmpl), current))
		assert.Equal(t, ampullav1.BackupTemplatePhaseFailed, current.Status.Phase)
		assert.Contains(t, current.Status.Message, "forbidden")
	}, eventually, tick)

	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "created"}, &corev1.ConfigMap{})
	assert.True(t, apierrors.IsNotFound(err), "nothing is written: %v", err)
}

// TestBackupTemplate_Invalid covers a template that cannot be applied, whatever the policy.
func TestBackupTemplate_Invalid(t *testing.T) {
	c := setup(t, testDefaults)
	ctx := t.Context()

	ns := testutil.TmpNamespace(t, c)
	tmpl := newTemplate(t, ns, "", "orders", nil)
	require.NoError(t, c.Create(ctx, tmpl))

	require.EventuallyWithT(t, func(t *assert.CollectT) {
		current := &ampullav1.BackupTemplate{}
		assert.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tmpl), current))
		assert.Equal(t, ampullav1.BackupTemplatePhaseFailed, current.Status.Phase)
		assert.Contains(t, current.Status.Message, "no metadata.name")
	}, eventually, tick)
}

// newTemplate builds a template of a ConfigMap named target. An empty target leaves the
// name out.
func newTemplate(t *testing.T, namespace, target, policy string, data map[string]any) *ampullav1.BackupTemplate {
	t.Helper()

	metadata := map[string]any{}
	if target != "" {
		metadata["name"] = target
	}
	raw, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   metadata,
		"data":       data,
	})
	require.NoError(t, err)

	name := target
	if name == "" {
		name = "unnamed"
	}
	return &ampullav1.BackupTemplate{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: ampullav1.BackupTemplateSpec{
			PolicyName: policy,
			Template:   runtime.RawExtension{Raw: raw},
		},
	}
}

// grantTemplateAccess is what chrysopoeia sets up in an instance namespace: a ServiceAccount
// allowed to write the kinds the instance requires. ConfigMaps stand in for those here.
func grantTemplateAccess(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	ctx := t.Context()

	require.NoError(t, c.Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: templateServiceAccount},
	}))
	require.NoError(t, c.Create(ctx, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "configmaps"},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"create", "patch"},
		}},
	}))
	require.NoError(t, c.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "configmaps"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "configmaps"},
		Subjects: []rbacv1.Subject{{
			Kind: rbacv1.ServiceAccountKind, Namespace: namespace, Name: templateServiceAccount,
		}},
	}))
}
