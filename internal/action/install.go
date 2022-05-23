package action

import (
	"context"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/helm-controller/internal/postrender"
	intrelease "github.com/fluxcd/helm-controller/internal/release"
	"github.com/fluxcd/helm-controller/internal/storage"
)

func Install(ctx context.Context, config *Configuration, obj *helmv2.HelmRelease, chrt *chart.Chart, vals chartutil.Values) (*release.Release, error) {
	cfg, _, err := config.newObservingConfig(ctx, observeInstall(obj), observeInstall(obj))
	if err != nil {
		return nil, err
	}

	install, err := newInstall(cfg, obj)
	if err != nil {
		return nil, err
	}

	if err := installCRDs(cfg, obj, chrt, install); err != nil {
		return nil, err
	}

	_, installErr := install.RunWithContext(ctx, chrt, vals.AsMap())
	return nil, installErr
}

func newInstall(config *action.Configuration, obj *helmv2.HelmRelease) (*action.Install, error) {
	install := action.NewInstall(config)

	install.ReleaseName = obj.GetReleaseName()
	install.Namespace = obj.GetReleaseNamespace()
	install.Timeout = obj.Spec.GetInstall().GetTimeout(obj.GetTimeout()).Duration
	install.Wait = !obj.Spec.GetInstall().DisableWait
	install.WaitForJobs = !obj.Spec.GetInstall().DisableWaitForJobs
	install.DisableHooks = obj.Spec.GetInstall().DisableHooks
	install.DisableOpenAPIValidation = obj.Spec.GetInstall().DisableOpenAPIValidation
	install.Replace = obj.Spec.GetInstall().Replace
	install.Devel = true

	if obj.Spec.TargetNamespace != "" {
		install.CreateNamespace = obj.Spec.GetInstall().CreateNamespace
	}

	renderer, err := postrender.BuildPostRenderers(obj)
	if err != nil {
		return nil, err
	}
	install.PostRenderer = renderer

	return install, nil
}

func installCRDs(config *action.Configuration, obj *helmv2.HelmRelease, chrt *chart.Chart, install *action.Install) error {
	policy, err := crdPolicyOrDefault(obj.Spec.GetInstall().CRDs)
	if err != nil {
		return err
	}
	if policy == helmv2.Skip || policy == helmv2.CreateReplace {
		install.SkipCRDs = true
	}
	if policy == helmv2.CreateReplace {
		crds := chrt.CRDObjects()
		if len(crds) > 0 {
			if err := applyCRDs(config, policy, chrt); err != nil {
				return err
			}
		}
	}
	return nil
}

func observeInstall(obj *helmv2.HelmRelease) storage.ObserveFunc {
	return func(rls *release.Release) {
		cur := obj.Status.Current
		if cur != nil {
			if cur.Name == rls.Name && cur.Version < rls.Version {
				// Add current to previous when we observe the first write of a
				// newer release.
				obj.Status.Previous = obj.Status.Current
			}
			if obj.Status.Current.Version <= rls.Version {
				// Overwrite current with newer release, or update it.
				obj.Status.Current = intrelease.ObservedToInfo(intrelease.ObserveRelease(rls))
			}
			if obj.Status.Previous.Version == rls.Version {
				// Write latest state of previous (e.g. status updates) to status.
				obj.Status.Previous = intrelease.ObservedToInfo(intrelease.ObserveRelease(rls))
			}
		}
	}
}
