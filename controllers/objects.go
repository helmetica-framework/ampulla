package controllers

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
	"github.com/helmetica-framework/ampulla/internal/backup"
)

// The objects ampulla applies belong to COSI and k8up, and neither ships generated apply
// configurations - controller-gen only writes them for types marked in their own package,
// which a dependency's are not. So the configurations are spelled out here rather than
// converted from a typed object: client.ApplyConfigurationFromUnstructured warns against
// the latter, because a typed struct cannot say whether a zero value was meant, while a
// map holds exactly the fields that were set and nothing else.
//
// The cost is that field names are strings. The envtest tests apply these against the real
// CRDs and read the results back typed, which is what catches a name the API would
// otherwise silently prune.

// objectMeta is the metadata of the one object ampulla creates rather than applies: the
// repository password secret, which is written once and never rewritten, so it stays a
// typed object with a typed owner reference.
func objectMeta(policy *backupsv1.BackupPolicy, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Namespace: policy.Namespace,
		Name:      name,
		Labels:    map[string]string{managedLabel: policy.Name},
	}
}

// objectFor is the shell every applied object shares: the policy's namespace, a label the
// manager's cache filters on, and the policy as controller so a deleted policy takes the
// object with it.
func objectFor(policy *backupsv1.BackupPolicy, owner map[string]any, apiVersion, kind, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":            name,
			"namespace":       policy.Namespace,
			"labels":          map[string]any{managedLabel: policy.Name},
			"ownerReferences": []any{owner},
		},
	}}
}

// controllerRef is the policy as a controller owner reference.
// controllerutil.SetControllerReference cannot be used: it takes a metav1.Object, and what
// is being built here is an apply configuration rather than an API object.
func controllerRef(policy *backupsv1.BackupPolicy, scheme *runtime.Scheme) (map[string]any, error) {
	gvk, err := apiutil.GVKForObject(policy, scheme)
	if err != nil {
		return nil, fmt.Errorf("looking up the policy's GVK: %w", err)
	}

	return map[string]any{
		"apiVersion":         gvk.GroupVersion().String(),
		"kind":               gvk.Kind,
		"name":               policy.Name,
		"uid":                string(policy.UID),
		"controller":         true,
		"blockOwnerDeletion": true,
	}, nil
}

func bucketClaimFor(policy *backupsv1.BackupPolicy, owner map[string]any, names backup.Names, cfg backup.Config) *unstructured.Unstructured {
	claim := objectFor(policy, owner, cosiAPIVersion, "BucketClaim", names.BucketClaim)

	claim.Object["spec"] = map[string]any{
		"bucketClassName": cfg.BucketClassName,
		"protocols":       []any{protocolS3},
	}
	return claim
}

// bucketAccessFor asks for one key pair on the policy's bucket. A BucketAccess can cover
// several claims; ampulla never needs that, because one policy owns exactly one bucket.
func bucketAccessFor(policy *backupsv1.BackupPolicy, owner map[string]any, names backup.Names, cfg backup.Config) *unstructured.Unstructured {
	access := objectFor(policy, owner, cosiAPIVersion, "BucketAccess", names.BucketAccess)

	access.Object["spec"] = map[string]any{
		"bucketAccessClassName": cfg.BucketAccessClassName,
		"protocol":              protocolS3,
		"bucketClaims": []any{map[string]any{
			"bucketClaimName": names.BucketClaim,
			// Backups are written and pruned, and a restore reads them back.
			"accessMode":       accessModeReadWrite,
			"accessSecretName": names.CredentialsSecret,
		}},
	}
	return access
}

// scheduleFor builds the k8up Schedule. With no label selector k8up backs up every PVC in
// the namespace, which is exactly the service's data and nothing else - the namespace
// holds one released chart.
//
// The backend reads the access key straight out of the Secret COSI wrote: v1alpha2 stores
// each piece of bucket info under its own key, which is the shape k8up's secret references
// need.
func scheduleFor(policy *backupsv1.BackupPolicy, owner map[string]any, names backup.Names, cfg backup.Config, bucket backup.Bucket) *unstructured.Unstructured {
	schedule := objectFor(policy, owner, k8upAPIVersion, "Schedule", names.Schedule)

	credential := func(key string) map[string]any {
		return map[string]any{"name": names.CredentialsSecret, "key": key}
	}

	spec := map[string]any{
		"backend": map[string]any{
			"repoPasswordSecretRef": map[string]any{
				"name": names.RepositorySecret,
				"key":  repoPasswordKey,
			},
			"s3": map[string]any{
				"endpoint":                 bucket.Endpoint,
				"bucket":                   bucket.ID,
				"accessKeyIDSecretRef":     credential(backup.AccessKeyIDKey),
				"secretAccessKeySecretRef": credential(backup.AccessSecretKeyKey),
			},
		},
		"backup": map[string]any{"schedule": cfg.Schedules.Backup},
	}

	if cfg.Schedules.Prune != "" {
		prune := map[string]any{"schedule": cfg.Schedules.Prune}
		if retention := retentionFor(cfg.Retention); len(retention) > 0 {
			prune["retention"] = retention
		}
		spec["prune"] = prune
	}

	if cfg.Schedules.Check != "" {
		spec["check"] = map[string]any{"schedule": cfg.Schedules.Check}
	}

	schedule.Object["spec"] = spec
	return schedule
}

// retentionFor keeps only the counts that were set. A zero would be applied as an explicit
// "keep none" and owned as such, where leaving it out means restic never prunes on it.
//
// The counts are int64 because that is the only integer an unstructured object holds; an
// int would be rejected when the apply is serialised.
func retentionFor(retention backupsv1.Retention) map[string]any {
	counts := map[string]int{
		"keepLast":    retention.KeepLast,
		"keepHourly":  retention.KeepHourly,
		"keepDaily":   retention.KeepDaily,
		"keepWeekly":  retention.KeepWeekly,
		"keepMonthly": retention.KeepMonthly,
		"keepYearly":  retention.KeepYearly,
	}

	set := map[string]any{}
	for key, count := range counts {
		if count > 0 {
			set[key] = int64(count)
		}
	}
	return set
}
