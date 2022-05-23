package action

import (
	"context"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	intrelease "github.com/fluxcd/helm-controller/internal/release"
	"github.com/fluxcd/helm-controller/internal/storage"
)

func Test(ctx context.Context, config *Configuration, obj *helmv2.HelmRelease) (*release.Release, error) {
	cfg, _, err := config.newObservingConfig(ctx, observeTest(obj))
	if err != nil {
		return nil, err
	}

	test := newTest(cfg, obj)
	return test.Run(obj.GetReleaseName())
}

func newTest(config *action.Configuration, obj *helmv2.HelmRelease) *action.ReleaseTesting {
	test := action.NewReleaseTesting(config)

	test.Namespace = obj.GetReleaseNamespace()
	test.Timeout = obj.Spec.GetTest().GetTimeout(obj.GetTimeout()).Duration

	return test
}

func observeTest(obj *helmv2.HelmRelease) storage.ObserveFunc {
	return func(obs *release.Release) {
		if cur := obj.Status.Current; cur != nil {
			if cur.Name == obs.Name && cur.Version == obs.Version {
				obj.Status.Current = intrelease.ObservedToInfo(intrelease.ObserveRelease(obs))
			}
		}
	}
}
