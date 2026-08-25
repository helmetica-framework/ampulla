package controllers

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	backupsv1 "github.com/helmetica-framework/ampulla/api/v1"
)

// Scheme is everything ampulla reads or writes. It is shared with the tests, so the
// envtest API server registers exactly what the controller does.
func Scheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(backupsv1.AddToScheme(scheme))
	return scheme
}
