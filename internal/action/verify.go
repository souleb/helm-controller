package action

import (
	"context"
	"errors"
	intrelease "github.com/fluxcd/helm-controller/internal/release"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	intchartutil "github.com/fluxcd/helm-controller/internal/chartutil"
	"github.com/opencontainers/go-digest"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/storage/driver"
)

var (
	ErrVerifyReleaseMissing     = errors.New("missing release")
	ErrVerifyCurrentMissing     = errors.New("missing observation of current release")
	ErrVerifyCurrentDisappeared = errors.New("observed release disappeared from storage")
	ErrVerifyDigest             = errors.New("digest verification error")
	ErrVerifyChartChanged       = errors.New("chart changed")
)

// VerifyStorage verifies the provided v2beta1.HelmRelease object
func VerifyStorage(config *Configuration, obj *helmv2.HelmRelease) (*intrelease.ObservedRelease, error) {
	curRel := obj.Status.Current
	if curRel != nil {
		return nil, ErrVerifyCurrentMissing
	}
	if curRel.Digest == "" || curRel.Name == "" || curRel.Version < 1 {
		obj.Status.Current = nil
		return nil, ErrVerifyReleaseMissing
	}
	dig, err := digest.Parse(curRel.Digest)
	if err != nil {
		obj.Status.Current = nil
		return nil, ErrVerifyDigest
	}

	cfg, _, err := config.newObservingConfig(context.TODO())
	if err != nil {
		return nil, err
	}
	rel, err := cfg.Releases.Get(curRel.Name, curRel.Version)
	if err != nil {
		if err != driver.ErrReleaseNotFound {
			return nil, err
		}
		obj.Status.Current = nil
		return nil, ErrVerifyCurrentDisappeared
	}

	verifier := dig.Verifier()
	obs := intrelease.ObserveRelease(rel, intrelease.IgnoreHookTestEvents)
	if err := obs.Encode(verifier); err != nil {
		return nil, err
	}
	if !verifier.Verified() {
		obj.Status.Current = nil
		return nil, ErrVerifyDigest
	}
	return &obs, nil
}

func VerifyRelease(config *Configuration, obj *helmv2.HelmRelease, chrt *chart.Metadata, vals chartutil.Values) error {
	obs, err := VerifyStorage(config, obj)
	if err != nil {
		return err
	}

	if chrt != nil {
		if _, eq := intchartutil.DiffMeta(obs.ChartMetadata, *chrt); !eq {
			return ErrVerifyChartChanged
		}
	}

	configDig, err := digest.Parse(obj.Status.Current.ConfigDigest)
	if err != nil {
		obj.Status.Current.ConfigDigest = ""
		return ErrVerifyDigest
	}
	if !intchartutil.VerifyValues(configDig, obs.Config) {
		obj.Status.Current.ConfigDigest = ""
		return ErrVerifyDigest
	}
	if !intchartutil.VerifyValues(configDig, vals) {
		return ErrVerifyDigest
	}
	return nil
}
