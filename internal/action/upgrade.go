package action

import (
	"context"

	v2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/helm-controller/internal/postrender"
	intrelease "github.com/fluxcd/helm-controller/internal/release"
	"github.com/fluxcd/helm-controller/internal/storage"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
)

func Upgrade(ctx context.Context, config *Configuration, obj *v2.HelmRelease, chrt *chart.Chart, vals chartutil.Values) (*release.Release, error) {
	cfg, _, err := config.newObservingConfig(ctx, observeUpgrade(obj))
	if err != nil {
		return nil, err
	}

	upgrade, err := newUpgrade(cfg, obj)
	if err != nil {
		return nil, err
	}

	if err := upgradeCRDs(cfg, obj, chrt); err != nil {
		return nil, err
	}

	_, upgradeErr := upgrade.RunWithContext(ctx, obj.GetReleaseName(), chrt, vals.AsMap())
	return nil, upgradeErr
}

func newUpgrade(config *action.Configuration, rel *v2.HelmRelease) (*action.Upgrade, error) {
	upgrade := action.NewUpgrade(config)

	upgrade.Namespace = rel.GetReleaseNamespace()
	upgrade.ResetValues = !rel.Spec.GetUpgrade().PreserveValues
	upgrade.ReuseValues = rel.Spec.GetUpgrade().PreserveValues
	upgrade.MaxHistory = rel.GetMaxHistory()
	upgrade.Timeout = rel.Spec.GetUpgrade().GetTimeout(rel.GetTimeout()).Duration
	upgrade.Wait = !rel.Spec.GetUpgrade().DisableWait
	upgrade.WaitForJobs = !rel.Spec.GetUpgrade().DisableWaitForJobs
	upgrade.DisableHooks = rel.Spec.GetUpgrade().DisableHooks
	upgrade.Force = rel.Spec.GetUpgrade().Force
	upgrade.CleanupOnFail = rel.Spec.GetUpgrade().CleanupOnFail
	upgrade.Devel = true

	renderer, err := postrender.BuildPostRenderers(rel)
	if err != nil {
		return nil, err
	}
	upgrade.PostRenderer = renderer

	return upgrade, err
}

func upgradeCRDs(config *action.Configuration, obj *v2.HelmRelease, chrt *chart.Chart) error {
	policy, err := crdPolicyOrDefault(obj.Spec.GetUpgrade().CRDs)
	if err != nil {
		return err
	}
	if policy != v2.Skip {
		crds := chrt.CRDObjects()
		if len(crds) > 0 {
			if err := applyCRDs(config, policy, chrt); err != nil {
				return err
			}
		}
	}
	return nil
}

func observeUpgrade(obj *v2.HelmRelease) storage.ObserveFunc {
	return func(rls *release.Release) {
		if cur := obj.Status.Current; cur != nil {
			if cur.Name == rls.Name && cur.Version < rls.Version {
				obj.Status.Previous = obj.Status.Current
			}
		}
		if obj.Status.Current == nil || obj.Status.Current.Version <= rls.Version {
			obj.Status.Current = intrelease.ObservedToInfo(intrelease.ObserveRelease(rls))
		}
		if obj.Status.Previous != nil && obj.Status.Previous.Version == rls.Version {
			obj.Status.Previous = intrelease.ObservedToInfo(intrelease.ObserveRelease(rls))
		}
	}
}
