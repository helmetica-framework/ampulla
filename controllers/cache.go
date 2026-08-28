package controllers

import (
	k8upv1 "github.com/k8up-io/k8up/v2/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	cosiv1alpha2 "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// managedSelector matches every object ampulla writes, whatever policy it belongs to. The
// label's value names that policy, so only the key's presence can be selected on here.
var managedSelector = func() labels.Selector {
	selector, err := labels.Parse(managedLabel)
	utilruntime.Must(err)
	return selector
}()

// CacheOptions restricts what the manager holds in memory. ampulla runs cluster-wide and
// touches Secrets, so this is what keeps it from caching every Secret in the cluster.
//
// The COSI credentials secret is deliberately outside this: it is written by COSI and
// carries none of ampulla's labels, so the controller reads it through the API reader
// instead.
func CacheOptions() cache.Options {
	managed := cache.ByObject{Label: managedSelector}

	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Secret{}:             managed,
			&cosiv1alpha2.BucketClaim{}:  managed,
			&cosiv1alpha2.BucketAccess{}: managed,
			&k8upv1.Schedule{}:           managed,
		},
	}
}
