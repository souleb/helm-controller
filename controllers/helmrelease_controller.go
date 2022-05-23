/*
Copyright 2020 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"time"

	action2 "github.com/fluxcd/helm-controller/internal/action"
	"github.com/fluxcd/helm-controller/internal/storage"
	"github.com/fluxcd/pkg/runtime/logger"
	"helm.sh/helm/v3/pkg/release"

	"github.com/hashicorp/go-retryablehttp"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	kuberecorder "k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/ratelimiter"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apiacl "github.com/fluxcd/pkg/apis/acl"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/acl"
	fluxClient "github.com/fluxcd/pkg/runtime/client"
	"github.com/fluxcd/pkg/runtime/events"
	"github.com/fluxcd/pkg/runtime/metrics"
	"github.com/fluxcd/pkg/runtime/predicates"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"

	v2 "github.com/fluxcd/helm-controller/api/v2beta1"
	intchartutil "github.com/fluxcd/helm-controller/internal/chartutil"
	"github.com/fluxcd/helm-controller/internal/kube"
	"github.com/fluxcd/helm-controller/internal/loader"
	intpredicates "github.com/fluxcd/helm-controller/internal/predicates"
	"github.com/fluxcd/helm-controller/internal/runner"
	"github.com/fluxcd/helm-controller/internal/util"
)

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases/finalizers,verbs=get;create;update;patch;delete
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=helmcharts,verbs=get;list;watch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=helmcharts/status,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// HelmReleaseReconciler reconciles a HelmRelease object
type HelmReleaseReconciler struct {
	client.Client
	httpClient          *retryablehttp.Client
	Config              *rest.Config
	Scheme              *runtime.Scheme
	requeueDependency   time.Duration
	EventRecorder       kuberecorder.EventRecorder
	MetricsRecorder     *metrics.Recorder
	NoCrossNamespaceRef bool
	ClientOpts          fluxClient.Options
	KubeConfigOpts      fluxClient.KubeConfigOptions
}

type HelmReleaseReconcilerOptions struct {
	MaxConcurrentReconciles   int
	HTTPRetry                 int
	DependencyRequeueInterval time.Duration
	RateLimiter               ratelimiter.RateLimiter
}

func (r *HelmReleaseReconciler) SetupWithManager(mgr ctrl.Manager, opts HelmReleaseReconcilerOptions) error {
	// Index the HelmRelease by the HelmChart references they point at
	if err := mgr.GetFieldIndexer().IndexField(context.TODO(), &v2.HelmRelease{}, v2.SourceIndexKey,
		func(o client.Object) []string {
			hr := o.(*v2.HelmRelease)
			return []string{
				fmt.Sprintf("%s/%s", hr.Spec.Chart.GetNamespace(hr.GetNamespace()), hr.GetHelmChartName()),
			}
		},
	); err != nil {
		return err
	}

	r.requeueDependency = opts.DependencyRequeueInterval

	// Configure the retryable http client used for fetching artifacts.
	// By default, it retries 10 times within a 3.5 minutes window.
	httpClient := retryablehttp.NewClient()
	httpClient.RetryWaitMin = 5 * time.Second
	httpClient.RetryWaitMax = 30 * time.Second
	httpClient.RetryMax = opts.HTTPRetry
	httpClient.Logger = nil
	r.httpClient = httpClient

	return ctrl.NewControllerManagedBy(mgr).
		For(&v2.HelmRelease{}, builder.WithPredicates(
			predicate.Or(predicate.GenerationChangedPredicate{}, predicates.ReconcileRequestedPredicate{}),
		)).
		Watches(
			&source.Kind{Type: &sourcev1.HelmChart{}},
			handler.EnqueueRequestsFromMapFunc(r.requestsForHelmChartChange),
			builder.WithPredicates(intpredicates.SourceRevisionChangePredicate{}),
		).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: opts.MaxConcurrentReconciles,
			RateLimiter:             opts.RateLimiter,
		}).
		Complete(r)
}

// ConditionError represents an error with a status condition reason attached.
type ConditionError struct {
	Reason string
	Err    error
}

func (c ConditionError) Error() string {
	return c.Err.Error()
}

func (r *HelmReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	log := ctrl.LoggerFrom(ctx)

	var hr v2.HelmRelease
	if err := r.Get(ctx, req.NamespacedName, &hr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// record suspension metrics
	defer r.recordSuspension(ctx, hr)

	// Add our finalizer if it does not exist
	if !controllerutil.ContainsFinalizer(&hr, v2.HelmReleaseFinalizer) {
		patch := client.MergeFrom(hr.DeepCopy())
		controllerutil.AddFinalizer(&hr, v2.HelmReleaseFinalizer)
		if err := r.Patch(ctx, &hr, patch); err != nil {
			log.Error(err, "unable to register finalizer")
			return ctrl.Result{}, err
		}
	}

	// Examine if the object is under deletion
	if !hr.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, hr)
	}

	// Return early if the HelmRelease is suspended.
	if hr.Spec.Suspend {
		log.Info("Reconciliation is suspended for this object")
		return ctrl.Result{}, nil
	}

	hr, result, err := r.reconcile(ctx, hr)

	// Update status after reconciliation.
	if updateStatusErr := r.patchStatus(ctx, &hr); updateStatusErr != nil {
		log.Error(updateStatusErr, "unable to update status after reconciliation")
		return ctrl.Result{Requeue: true}, updateStatusErr
	}

	// Record ready status
	r.recordReadiness(ctx, hr)

	// Log reconciliation duration
	durationMsg := fmt.Sprintf("reconcilation finished in %s", time.Now().Sub(start).String())
	if result.RequeueAfter > 0 {
		durationMsg = fmt.Sprintf("%s, next run in %s", durationMsg, result.RequeueAfter.String())
	}
	log.Info(durationMsg)

	return result, err
}

func (r *HelmReleaseReconciler) reconcile(ctx context.Context, hr v2.HelmRelease) (v2.HelmRelease, ctrl.Result, error) {
	reconcileStart := time.Now()
	log := ctrl.LoggerFrom(ctx)
	// Record the value of the reconciliation request, if any
	if v, ok := meta.ReconcileAnnotationValue(hr.GetAnnotations()); ok {
		hr.Status.SetLastHandledReconcileRequest(v)
	}

	// Observe HelmRelease generation.
	if hr.Status.ObservedGeneration != hr.Generation {
		hr.Status.ObservedGeneration = hr.Generation
		hr = v2.HelmReleaseProgressing(hr)
		if updateStatusErr := r.patchStatus(ctx, &hr); updateStatusErr != nil {
			log.Error(updateStatusErr, "unable to update status after generation update")
			return hr, ctrl.Result{Requeue: true}, updateStatusErr
		}
		// Record progressing status
		r.recordReadiness(ctx, hr)
	}

	// Record reconciliation duration
	if r.MetricsRecorder != nil {
		objRef, err := reference.GetReference(r.Scheme, &hr)
		if err != nil {
			return hr, ctrl.Result{Requeue: true}, err
		}
		defer r.MetricsRecorder.RecordDuration(*objRef, reconcileStart)
	}

	// Get HelmChart object for release
	hc, err := r.getHelmChart(ctx, &hr)
	if err != nil {
		if acl.IsAccessDenied(err) {
			log.Error(err, "access denied to cross-namespace source")
			r.event(ctx, hr, hr.Status.LastAttemptedRevision, events.EventSeverityError, err.Error())
			return v2.HelmReleaseNotReady(hr, apiacl.AccessDeniedReason, err.Error()),
				ctrl.Result{RequeueAfter: hr.Spec.Interval.Duration}, nil
		}

		msg := fmt.Sprintf("chart reconciliation failed: %s", err.Error())
		r.event(ctx, hr, hr.Status.LastAttemptedRevision, events.EventSeverityError, msg)
		return v2.HelmReleaseNotReady(hr, v2.ArtifactFailedReason, msg), ctrl.Result{Requeue: true}, err
	}

	// Check chart readiness
	if hc.Generation != hc.Status.ObservedGeneration || !apimeta.IsStatusConditionTrue(hc.Status.Conditions, meta.ReadyCondition) {
		msg := fmt.Sprintf("HelmChart '%s/%s' is not ready", hc.GetNamespace(), hc.GetName())
		r.event(ctx, hr, hr.Status.LastAttemptedRevision, events.EventSeverityInfo, msg)
		log.Info(msg)
		// Do not requeue immediately, when the artifact is created
		// the watcher should trigger a reconciliation.
		return v2.HelmReleaseNotReady(hr, v2.ArtifactFailedReason, msg), ctrl.Result{RequeueAfter: hc.Spec.Interval.Duration}, nil
	}

	// Check dependencies
	if len(hr.Spec.DependsOn) > 0 {
		if err := r.checkDependencies(hr); err != nil {
			msg := fmt.Sprintf("dependencies do not meet ready condition (%s), retrying in %s",
				err.Error(), r.requeueDependency.String())
			r.event(ctx, hr, hc.GetArtifact().Revision, events.EventSeverityInfo, msg)
			log.Info(msg)

			// Exponential backoff would cause execution to be prolonged too much,
			// instead we requeue on a fixed interval.
			return v2.HelmReleaseNotReady(hr,
				v2.DependencyNotReadyReason, err.Error()), ctrl.Result{RequeueAfter: r.requeueDependency}, nil
		}
		log.Info("all dependencies are ready, proceeding with release")
	}

	// Compose values
	values, err := intchartutil.ChartValuesFromReferences(ctx, r.Client, hr.Namespace, hr.GetValues(), hr.Spec.ValuesFrom...)
	if err != nil {
		r.event(ctx, hr, hr.Status.LastAttemptedRevision, events.EventSeverityError, err.Error())
		return v2.HelmReleaseNotReady(hr, v2.InitFailedReason, err.Error()), ctrl.Result{Requeue: true}, nil
	}

	// Load chart from artifact
	chart, err := loader.SecureLoadChartFromURL(r.httpClient, hc.GetArtifact().URL, hc.GetArtifact().Checksum)
	if err != nil {
		r.event(ctx, hr, hr.Status.LastAttemptedRevision, events.EventSeverityError, err.Error())
		return v2.HelmReleaseNotReady(hr, v2.ArtifactFailedReason, err.Error()), ctrl.Result{Requeue: true}, nil
	}

	// Reconcile Helm release
	reconciledHr, reconcileErr := r.reconcileRelease(ctx, *hr.DeepCopy(), chart, values)
	if reconcileErr != nil {
		r.event(ctx, hr, hc.GetArtifact().Revision, events.EventSeverityError,
			fmt.Sprintf("reconciliation failed: %s", reconcileErr.Error()))
	}
	return reconciledHr, ctrl.Result{RequeueAfter: hr.Spec.Interval.Duration}, reconcileErr
}

func (r *HelmReleaseReconciler) reconcileReleases(ctx context.Context, config *action2.Configuration, obj *v2.HelmRelease,
	chrt *chart.Chart, vals chartutil.Values) (ctrl.Result, error) {

	obs, err := action2.Verify(config, obj, chrt.Metadata, vals)
	switch err {
	case action2.ErrVerifyReleaseMissing, action2.ErrVerifyCurrentDisappeared:
		return r.reconcileInstall(ctx, config, obj, chrt, vals)
	case action2.ErrVerifyDigest, action2.ErrVerifyCurrentMissing, action2.ErrVerifyChartChanged:
		return r.reconcileUpgrade(ctx, config, obj, chrt, vals)
	case nil:
		return ctrl.Result{},
	}
	if err != nil {
		switch expr {

		}
	} 
}

func (r *HelmReleaseReconciler) reconcileInstall(ctx context.Context, config *action2.Configuration, obj *v2.HelmRelease,
	chrt *chart.Chart, vals chartutil.Values) (ctrl.Result, error) {
	// Run the installation action.
	_, actionErr := action2.Install(ctx, config, obj, chrt, vals)

	//
	// Confirm observation was made if installation is successful.
	obs, err = config.Observer().LastObservation(obj.GetReleaseName())
	if actionErr == nil {
		if err != nil {
			// Do not count this as an installation error, as it is not expected to
			// be the cause of user input.
			if err == storage.ErrReleaseNotObserved {
				err = fmt.Errorf("%w: expected observation of install", err)
			}
			return ctrl.Result{}, err
		}
		if obs.HasStatus(release.StatusDeployed) {
			// Mark the applied revision as observed from the release
			// and reset any failure counts.
			obj.Status.LastAppliedRevision = obs.ChartMetadata.Version
			obj.Status.InstallFailures = 0
		}
	}

	// Mark any install failure.
	if actionErr != nil || !obs.HasStatus(release.StatusDeployed) {
		ctrl.LoggerFrom(ctx).V(logger.DebugLevel).Info(fmt.Sprintf("install error:"+
			"release %s has status %s", obs.Name, obs.Info.Status.String()))
		obj.Status.InstallFailures++
	}

	// Make note of checksum.
	obj.Status.LastObservedReleaseChecksum = obs.Digest().String()

	// Return an empty result (as we are not expecting to install again),
	// or an error.
	return ctrl.Result{}, actionErr
}

func (r *HelmReleaseReconciler) reconcileUpgrade(ctx context.Context, config *action2.Configuration, obj *v2.HelmRelease,
	chrt *chart.Chart, vals chartutil.Values) (ctrl.Result, error) {

	// Make a new observation of the current state.
	prevRel, err := config.Observer().ObserveLastRelease(obj.GetReleaseName())
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	checksumMatch := prevRel.Checksum() == obj.Status.LastObservedReleaseChecksum
	valuesMatch := util.ValuesChecksum(sha1.New(), prevRel.Config) == util.ValuesChecksum(sha1.New(), vals)
	chartMatch := prevRel.ChartMetadata.Version == obj.Status.LastAttemptedRevision
	if checksumMatch && valuesMatch && chartMatch {
		ctrl.LoggerFrom(ctx).V(logger.DebugLevel).Info(fmt.Sprintf("upgrade error:"+
			"release %s has status %s", prevRel.Name, prevRel.Info.Status.String()))
		return ctrl.Result{RequeueAfter: obj.GetRequeueAfter()}, nil
	}

	_, actionErr := action2.Upgrade(ctx, config, obj, chrt, vals)
	// Confirm observation was made if upgrade is successful.
	rel, err := config.Observer().LastObservation(obj.GetReleaseName())
	if actionErr != nil {
		if err == storage.ErrReleaseNotObserved {
			// Do not count this as an upgrade error, as it is not expected to
			// be the cause of user input.
			return ctrl.Result{}, fmt.Errorf("%w: expected observation of upgrade", err)
		}
	}

	// Take note of checksum object. When obs is empty, this simply resets
	// to an empty string.
	obj.Status.LastObservedReleaseChecksum = rel.Checksum()
	obj.Status.LastAttemptedRevision = rel.ChartMetadata.Version
	obj.Status.LastAttemptedValuesChecksum = util.ValuesChecksum(sha1.New(), rel.Config)

	// Log status for now.
	// TODO(hidde): probably reflect more in either Status, Events or both.
	if prevRel.Info.Status != release.StatusDeployed {
		ctrl.LoggerFrom(ctx).V(logger.DebugLevel).Info(fmt.Sprintf("install error:"+
			"release %s has status %s", prevRel.Name, prevRel.Info.Status.String()))
	}

	// Return an empty result (as we are not expecting to install again),
	// or an error.
	return ctrl.Result{}, actionErr
}

func (r *HelmReleaseReconciler) reconcileTest(ctx context.Context, config *action2.Configuration, obj *v2.HelmRelease) (ctrl.Result, error) {
	if !obj.Spec.GetTest().Enable {
		// Skip any testing as it is not enabled.
		return ctrl.Result{}, nil
	}

	// Confirm we do have a release in storage.
	obs, err := config.Observer().ObserveLastRelease(obj.GetReleaseName())
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	if obs.Checksum() != obj.Status.LastObservedReleaseChecksum {
		// Checksum mismatch, assume we first need to settle this and return.
		return ctrl.Result{}, nil
	}
	if !obs.HasStatus(release.StatusDeployed) || obs.HasBeenTested() {
		// Release is in a failed state or has already been tested.
		// We should either wait for a new release, or a rollback.
		return ctrl.Result{}, nil
	}

	// Run tests for the release and observe the result
	_, actionErr := action2.Test(ctx, config, obj)
	lastObs, err := config.Observer().LastObservation(obj.GetReleaseName())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to observe release after running test: %w", err)
	}

	// As Helm just targets the latest release version, we have to confirm
	// we actually tested the earlier observed version. If this mismatches,
	// something modified the storage between our last observation and the
	// test trigger.
	if obs.Version != lastObs.Version {
		return ctrl.Result{}, fmt.Errorf("observed test release version %d did not match expected %d",
			lastObs.Version, obs.Version)
	}

	if lastObs.Checksum() == "" {
		return ctrl.Result{}, fmt.Errorf("expected observed release test to have checksum")
	}
	obj.Status.LastObservedReleaseChecksum = lastObs.Checksum()

	// Confirm release has been tested, this does not confirm test results.
	if !lastObs.HasBeenTested() {
		if actionErr != nil {
			return ctrl.Result{}, actionErr
		}
		return ctrl.Result{}, fmt.Errorf("expected release to be tested")
	}

	if lastObs.HasFailedTests() {
		// Tests failed.
		return ctrl.Result{}, fmt.Errorf("failed tests")
	}

	return ctrl.Result{}, nil
}

func (r *HelmReleaseReconciler) reconcileRollback(ctx context.Context, config *action2.Configuration, obj *v2.HelmRelease) (ctrl.Result, error) {
	// Confirm we do have a release in storage.
	obs, err := config.Observer().ObserveLastRelease(obj.GetReleaseName())
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	if checksum := obs.Checksum(); checksum == "" || checksum != obj.Status.LastObservedReleaseChecksum {
		return ctrl.Result{}, nil
	}

	if obs.HasStatus(release.StatusDeployed) && !obs.HasFailedTests() {
		return ctrl.Result{}, nil
	}

	if obj.Status.LastReleaseRevision < 1 || obs.Version <= 1 {
		// We are unable to perform any rollback if we do not have a last
		// successful revision, or if there aren't more than two revisions.
		return ctrl.Result{}, nil
	}
	if obj.Status.LastReleaseRevision >= obs.Version {
		return ctrl.Result{}, fmt.Errorf("can not rollback to release version higher than or equal to current")
	}

	strategy := obj.Spec.GetUpgrade().GetRemediation()
	if strategy.GetStrategy() != v2.RollbackRemediationStrategy {
		return ctrl.Result{}, nil
	}
	if strategy.RetriesExhausted(*obj) || !strategy.MustRemediateLastFailure() {
		return ctrl.Result{}, nil
	}

	prevObs, err := config.Observer().GetObservedVersion(obj.GetReleaseName(), obj.Status.LastReleaseRevision)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to observe previous successful release with version %d: %w",
			obj.Status.LastReleaseRevision, err)
	}
	if obj.Status.LastReleaseChecksum != "" && prevObs.Checksum() != obj.Status.LastReleaseChecksum {
		return ctrl.Result{}, fmt.Errorf("cannot rollback to release with version %d: checksum mismatch (%s => %s)",
			prevObs.Version, obj.Status.LastReleaseChecksum, prevObs.Checksum())
	}

	actionErr := action2.Rollback(ctx, config, obj, obj.Status.LastReleaseRevision)
	lastObs, err := config.Observer().LastObservation(obj.GetReleaseName())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to observe release after rollback: %w", err)
	}
	// TODO: check if failed
	// TODO: record checksum if succeeded
	if actionErr == nil {

	}
	if lastObs.
	if lastObs.
}

func (r *HelmReleaseReconciler) reconcileUninstall(ctx context.Context, config *action2.Configuration, obj *v2.HelmRelease) (ctrl.Result, error) {
	// Confirm we do have a release in storage.
	obs, err := config.Observer().ObserveLastRelease(obj.GetReleaseName())
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if checksum := obs.Checksum(); checksum == "" || checksum != obj.Status.LastObservedReleaseChecksum {
		return ctrl.Result{}, nil
	}

	if obj.DeletionTimestamp == nil {
		if obs.HasStatus(release.StatusDeployed) && !obs.HasFailedTests() {
			return ctrl.Result{}, nil
		}

		var strategy v2.Remediation = obj.Spec.Install.Remediation
		if obs.Version > 1 && obj.Status.LastAppliedRevision != "" {
			strategy = obj.Spec.Upgrade.Remediation
		}
		if strategy.GetStrategy() != v2.UninstallRemediationStrategy {
			return ctrl.Result{}, nil
		}
		if strategy.RetriesExhausted(*obj) || !strategy.MustRemediateLastFailure() {
			return ctrl.Result{}, nil
		}
	}

	_, actionErr := action2.Uninstall(ctx, config, obj)
	lastObs, err := config.Observer().LastObservation(obj.GetReleaseName())
	if err != nil {
		return ctrl.Result{}, err
	}
}

func (r *HelmReleaseReconciler) checkDependencies(hr v2.HelmRelease) error {
	for _, d := range hr.Spec.DependsOn {
		if d.Namespace == "" {
			d.Namespace = hr.GetNamespace()
		}
		dName := types.NamespacedName{
			Namespace: d.Namespace,
			Name:      d.Name,
		}
		var dHr v2.HelmRelease
		err := r.Get(context.Background(), dName, &dHr)
		if err != nil {
			return fmt.Errorf("unable to get '%s' dependency: %w", dName, err)
		}

		if len(dHr.Status.Conditions) == 0 || dHr.Generation != dHr.Status.ObservedGeneration {
			return fmt.Errorf("dependency '%s' is not ready", dName)
		}

		if !apimeta.IsStatusConditionTrue(dHr.Status.Conditions, meta.ReadyCondition) {
			return fmt.Errorf("dependency '%s' is not ready", dName)
		}
	}
	return nil
}

func (r *HelmReleaseReconciler) buildRESTClientGetter(ctx context.Context, hr v2.HelmRelease) (genericclioptions.RESTClientGetter, error) {
	opts := []kube.ClientGetterOption{kube.WithClientOptions(r.ClientOpts)}
	if hr.Spec.ServiceAccountName != "" {
		opts = append(opts, kube.WithImpersonate(hr.Spec.ServiceAccountName))
	}
	if hr.Spec.KubeConfig != nil {
		secretName := types.NamespacedName{
			Namespace: hr.GetNamespace(),
			Name:      hr.Spec.KubeConfig.SecretRef.Name,
		}
		var secret corev1.Secret
		if err := r.Get(ctx, secretName, &secret); err != nil {
			return nil, fmt.Errorf("could not find KubeConfig secret '%s': %w", secretName, err)
		}
		kubeConfig, err := kube.ConfigFromSecret(&secret, hr.Spec.KubeConfig.SecretRef.Key)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kube.WithKubeConfig(kubeConfig, r.KubeConfigOpts))
	}
	return kube.BuildClientGetter(hr.GetReleaseNamespace(), opts...)
}

// getHelmChart retrieves the v1beta2.HelmChart for the given
// v2beta1.HelmRelease using the name that is advertised in the status
// object. It returns the v1beta2.HelmChart, or an error.
func (r *HelmReleaseReconciler) getHelmChart(ctx context.Context, hr *v2.HelmRelease) (*sourcev1.HelmChart, error) {
	namespace, name := hr.Status.GetHelmChart()
	chartName := types.NamespacedName{Namespace: namespace, Name: name}
	if r.NoCrossNamespaceRef && chartName.Namespace != hr.Namespace {
		return nil, acl.AccessDeniedError(fmt.Sprintf("can't access '%s/%s', cross-namespace references have been blocked",
			hr.Spec.Chart.Spec.SourceRef.Kind, types.NamespacedName{
				Namespace: hr.Spec.Chart.Spec.SourceRef.Namespace,
				Name:      hr.Spec.Chart.Spec.SourceRef.Name,
			}))
	}
	hc := sourcev1.HelmChart{}
	if err := r.Client.Get(ctx, chartName, &hc); err != nil {
		return nil, err
	}
	return &hc, nil
}

// reconcileDelete deletes the v1beta2.HelmChart of the v2beta1.HelmRelease,
// and uninstalls the Helm release if the resource has not been suspended.
func (r *HelmReleaseReconciler) reconcileDelete(ctx context.Context, hr v2.HelmRelease) (ctrl.Result, error) {
	r.recordReadiness(ctx, hr)

	// Only uninstall the Helm Release if the resource is not suspended.
	if !hr.Spec.Suspend {
		getter, err := r.buildRESTClientGetter(ctx, hr)
		if err != nil {
			return ctrl.Result{}, err
		}
		run, err := runner.NewRunner(getter, hr.GetStorageNamespace(), ctrl.LoggerFrom(ctx))
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := run.Uninstall(hr); err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
			return ctrl.Result{}, err
		}
		ctrl.LoggerFrom(ctx).Info("uninstalled Helm release for deleted resource")

	} else {
		ctrl.LoggerFrom(ctx).Info("skipping Helm uninstall for suspended resource")
	}

	// Remove our finalizer from the list and update it.
	controllerutil.RemoveFinalizer(&hr, v2.HelmReleaseFinalizer)
	if err := r.Update(ctx, &hr); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *HelmReleaseReconciler) patchStatus(ctx context.Context, hr *v2.HelmRelease) error {
	key := client.ObjectKeyFromObject(hr)
	latest := &v2.HelmRelease{}
	if err := r.Client.Get(ctx, key, latest); err != nil {
		return err
	}
	return r.Client.Status().Patch(ctx, hr, client.MergeFrom(latest))
}

func (r *HelmReleaseReconciler) requestsForHelmChartChange(o client.Object) []reconcile.Request {
	hc, ok := o.(*sourcev1.HelmChart)
	if !ok {
		panic(fmt.Sprintf("Expected a HelmChart, got %T", o))
	}
	// If we do not have an artifact, we have no requests to make
	if hc.GetArtifact() == nil {
		return nil
	}

	ctx := context.Background()
	var list v2.HelmReleaseList
	if err := r.List(ctx, &list, client.MatchingFields{
		v2.SourceIndexKey: client.ObjectKeyFromObject(hc).String(),
	}); err != nil {
		return nil
	}

	var reqs []reconcile.Request
	for _, i := range list.Items {
		// If the revision of the artifact equals to the last attempted revision,
		// we should not make a request for this HelmRelease
		if hc.GetArtifact().Revision == i.Status.LastAttemptedRevision {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&i)})
	}
	return reqs
}

// event emits a Kubernetes event and forwards the event to notification controller if configured.
func (r *HelmReleaseReconciler) event(_ context.Context, hr v2.HelmRelease, revision, severity, msg string) {
	var meta map[string]string
	if revision != "" {
		meta = map[string]string{v2.GroupVersion.Group + "/revision": revision}
	}
	eventtype := "Normal"
	if severity == events.EventSeverityError {
		eventtype = "Warning"
	}
	r.EventRecorder.AnnotatedEventf(&hr, meta, eventtype, severity, msg)
}

func (r *HelmReleaseReconciler) recordSuspension(ctx context.Context, hr v2.HelmRelease) {
	if r.MetricsRecorder == nil {
		return
	}
	log := ctrl.LoggerFrom(ctx)

	objRef, err := reference.GetReference(r.Scheme, &hr)
	if err != nil {
		log.Error(err, "unable to record suspended metric")
		return
	}

	if !hr.DeletionTimestamp.IsZero() {
		r.MetricsRecorder.RecordSuspend(*objRef, false)
	} else {
		r.MetricsRecorder.RecordSuspend(*objRef, hr.Spec.Suspend)
	}
}

func (r *HelmReleaseReconciler) recordReadiness(ctx context.Context, hr v2.HelmRelease) {
	if r.MetricsRecorder == nil {
		return
	}

	objRef, err := reference.GetReference(r.Scheme, &hr)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "unable to record readiness metric")
		return
	}
	if rc := apimeta.FindStatusCondition(hr.Status.Conditions, meta.ReadyCondition); rc != nil {
		r.MetricsRecorder.RecordCondition(*objRef, *rc, !hr.DeletionTimestamp.IsZero())
	} else {
		r.MetricsRecorder.RecordCondition(*objRef, metav1.Condition{
			Type:   meta.ReadyCondition,
			Status: metav1.ConditionUnknown,
		}, !hr.DeletionTimestamp.IsZero())
	}
}
