package controllers

import (
	k8upv1 "github.com/k8up-io/k8up/v2/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	cosiv1alpha2 "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha2"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
)

// Scheme is everything ampulla reads or writes.
func Scheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(backupsv1.AddToScheme(scheme))
	utilruntime.Must(cosiv1alpha2.AddToScheme(scheme))
	utilruntime.Must(k8upv1.AddToScheme(scheme))
	return scheme
}
