package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ampullav1 "github.com/helmetica-framework/ampulla/api/v1"
	backupsacv1 "github.com/helmetica-framework/ampulla/applyconfiguration/api/v1"
	"github.com/helmetica-framework/ampulla/internal/backup"
)

const (
	// templateFieldOwner is the field manager of everything this controller applies.
	templateFieldOwner = client.FieldOwner("ampulla:backuptemplate")

	// templateRetry is how often a template waiting on something outside its own spec looks
	// again: permissions not granted yet, which are not watched.
	templateRetry = 30 * time.Second
)

// BackupTemplateManager reconciles BackupTemplate objects.
type BackupTemplateManager struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Log      logr.Logger

	// Config is what the clients impersonating ServiceAccount are built from.
	Config *rest.Config
	// ServiceAccount is impersonated, in the template's namespace, to write the rendered
	// object. ampulla is granted no access to the operators' kinds itself, so a template
	// can only write what that account may.
	ServiceAccount string
}

// templateState is what a template should come to: the phase to report, how long to wait
// before looking again, and the rendered object to apply - nil while there is nothing to
// apply yet.
type templateState struct {
	Phase   ampullav1.BackupTemplatePhase
	Message string
	Requeue time.Duration
	Object  *unstructured.Unstructured
}

// The resourceNames have to match --template-service-account.
// +kubebuilder:rbac:groups=ampulla.helmetica.io,resources=backuptemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=ampulla.helmetica.io,resources=backuptemplates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=impersonate,resourceNames=instance-admin

// Reconcile renders a BackupTemplate with its policy's bucket and applies the result.
func (r *BackupTemplateManager) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("backuptemplate", req.NamespacedName)

	tmpl := &ampullav1.BackupTemplate{}
	err := r.Get(ctx, req.NamespacedName, tmpl)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("template is gone, nothing to do")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !tmpl.GetDeletionTimestamp().IsZero() {
		log.V(1).Info("template is being deleted, nothing to do")
		return ctrl.Result{}, nil
	}

	state, err := r.desiredState(ctx, log, tmpl)
	if err != nil {
		return ctrl.Result{}, err
	}

	if state.Object != nil {
		err := r.applyObject(ctx, tmpl.Namespace, state.Object)
		switch {
		case apierrors.IsForbidden(err):
			// The fix is RBAC, which is not watched, so it is retried rather than left to
			// a spec change.
			r.Recorder.Eventf(tmpl, nil, corev1.EventTypeWarning, "Forbidden", "Backup", "%s", err)
			state = templateState{Phase: ampullav1.BackupTemplatePhaseFailed, Message: err.Error(), Requeue: templateRetry}
		case err != nil:
			return ctrl.Result{}, fmt.Errorf("applying %s %s: %w", state.Object.GetKind(), state.Object.GetName(), err)
		}
	}

	want := ampullav1.BackupTemplateStatus{
		Phase:              state.Phase,
		Message:            state.Message,
		ObservedGeneration: tmpl.Generation,
	}

	if tmpl.Status != want {
		if tmpl.Status.Phase != state.Phase {
			log.Info("template phase changed", "from", tmpl.Status.Phase, "to", state.Phase)
		}
		status := backupsacv1.BackupTemplate(tmpl.Name, tmpl.Namespace).
			WithStatus(templateStatusFor(want))
		if err := r.Status().Apply(ctx, status, templateFieldOwner, client.ForceOwnership); err != nil {
			return ctrl.Result{}, fmt.Errorf("applying template status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: state.Requeue}, nil
}

func templateStatusFor(status ampullav1.BackupTemplateStatus) *backupsacv1.BackupTemplateStatusApplyConfiguration {
	ac := backupsacv1.BackupTemplateStatus().
		WithPhase(status.Phase).
		WithObservedGeneration(status.ObservedGeneration)

	if status.Message != "" {
		ac.WithMessage(status.Message)
	}
	return ac
}

// desiredState renders the template once its policy's bucket is ready. It writes nothing:
// the rendered object is returned for Reconcile to apply.
func (r *BackupTemplateManager) desiredState(ctx context.Context, log logr.Logger, tmpl *ampullav1.BackupTemplate) (templateState, error) {
	obj, err := templateObject(tmpl)
	if err != nil {
		// Only a spec change fixes this, and a spec change reconciles again.
		r.Recorder.Eventf(tmpl, nil, corev1.EventTypeWarning, "NotActionable", "Backup", "%s", err)
		return templateState{Phase: ampullav1.BackupTemplatePhaseFailed, Message: err.Error()}, nil
	}

	policy := &ampullav1.BackupPolicy{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: tmpl.Namespace, Name: tmpl.Spec.PolicyName}, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return templateState{
				Phase:   ampullav1.BackupTemplatePhasePending,
				Message: fmt.Sprintf("waiting for BackupPolicy %s", tmpl.Spec.PolicyName),
			}, nil
		}
		return templateState{}, err
	}
	if policy.Status.Phase != ampullav1.BackupPolicyPhaseReady {
		log.V(1).Info("waiting for the policy's bucket", "backuppolicy", policy.Name)
		return templateState{
			Phase:   ampullav1.BackupTemplatePhasePending,
			Message: fmt.Sprintf("waiting for BackupPolicy %s to be ready", policy.Name),
		}, nil
	}

	backup.Render(obj.Object, policy.Status)

	if err := controllerutil.SetControllerReference(tmpl, obj, r.Scheme); err != nil {
		return templateState{}, fmt.Errorf("owning %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}

	return templateState{
		Phase:   ampullav1.BackupTemplatePhaseReady,
		Message: fmt.Sprintf("%s %s backs up to %s", obj.GetKind(), obj.GetName(), policy.Status.Bucket),
		Requeue: resyncInterval,
		Object:  obj,
	}, nil
}

// applyObject server-side-applies the rendered object as the template ServiceAccount of
// namespace.
func (r *BackupTemplateManager) applyObject(ctx context.Context, namespace string, obj *unstructured.Unstructured) error {
	c, err := r.impersonating(namespace)
	if err != nil {
		return err
	}
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj), templateFieldOwner, client.ForceOwnership)
}

// impersonating is a client acting as the template ServiceAccount of namespace.
func (r *BackupTemplateManager) impersonating(namespace string) (client.Client, error) {
	cfg := rest.CopyConfig(r.Config)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: fmt.Sprintf("system:serviceaccount:%s:%s", namespace, r.ServiceAccount),
	}
	c, err := client.New(cfg, client.Options{Scheme: r.Scheme, Mapper: r.RESTMapper()})
	if err != nil {
		return nil, fmt.Errorf("building a client impersonating %s/%s: %w", namespace, r.ServiceAccount, err)
	}
	return c, nil
}

// templatesFor maps a policy to the templates rendered with its bucket.
func (r *BackupTemplateManager) templatesFor(ctx context.Context, policy client.Object) []reconcile.Request {
	templates := &ampullav1.BackupTemplateList{}
	if err := r.List(ctx, templates, client.InNamespace(policy.GetNamespace())); err != nil {
		r.Log.Error(err, "listing templates", "namespace", policy.GetNamespace())
		return nil
	}

	var requests []reconcile.Request
	for _, tmpl := range templates.Items {
		if tmpl.Spec.PolicyName == policy.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&tmpl)})
		}
	}
	return requests
}

// SetupWithManager wires the controller: watch BackupTemplate, plus the policies they are
// rendered with, so a bucket becoming ready brings the template straight back.
func (r *BackupTemplateManager) SetupWithManager(name string, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&ampullav1.BackupTemplate{}).
		Watches(&ampullav1.BackupPolicy{}, handler.EnqueueRequestsFromMapFunc(r.templatesFor)).
		Complete(r)
}
