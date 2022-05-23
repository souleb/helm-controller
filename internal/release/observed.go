package release

import (
	"encoding/json"
	v2 "github.com/fluxcd/helm-controller/api/v2beta1"
	intchartutil "github.com/fluxcd/helm-controller/internal/chartutil"
	"github.com/mitchellh/copystructure"
	"github.com/opencontainers/go-digest"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"io"
)

// ObservedRelease is a copy of a release.Release as observed to be written to
// a Helm storage driver by a storage.Observator. The object is detached from
// the Helm storage object, and mutations to it do not change the underlying
// release object.
type ObservedRelease struct {
	// Name of the release.
	Name string `json:"name"`
	// Version of the release, at times also called revision.
	Version int `json:"version"`
	// Info provides information about the release.
	Info release.Info `json:"info"`
	// ChartMetadata contains the current Chartfile data of the release.
	ChartMetadata chart.Metadata `json:"chartMetadata"`
	// Config is the set of extra Values added to the chart.
	// These values override the default values inside the chart.
	Config map[string]interface{} `json:"config"`
	// Manifest is the string representation of the rendered template.
	// It is omitted from the JSON representation of the object for performance
	// reasons. ManifestChecksum can be used to notice changes, after which
	// Manifest can be inspected while loaded.
	Manifest string `json:"manifest"`
	// Hooks are all the hooks declared for this release, and the current
	// state they are in.
	Hooks []release.Hook `json:"hooks"`
	// Namespace is the Kubernetes namespace of the release.
	Namespace string `json:"namespace"`
	// Labels of the release.
	Labels map[string]string `json:"labels"`
}

// Encode JSON encodes the ObservedRelease and writes it into the given writer.
func (o ObservedRelease) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	if err := enc.Encode(o); err != nil {
		return err
	}
	return nil
}

// DataFilter allows for filtering data from the returned ObservedRelease while
// making an observation.
type DataFilter func(rel *ObservedRelease)

// IgnoreHookTestEvents ignores test event hooks. This allows to exclude it
// while e.g. generating a digest for the object. To prevent manual test
// triggers from a user to interfere with the checksum.
func IgnoreHookTestEvents(rel *ObservedRelease) {
	if len(rel.Hooks) > 0 {
		hooks := rel.Hooks
		for _, h := range rel.Hooks {
			for k, e := range h.Events {
				if e == release.HookTest {
					hooks = append(hooks[:k], hooks[k+1:]...)
				}
			}
		}
		rel.Hooks = hooks
	}
}

// ObserveRelease deep copies the values from the provided release.Release
// into a new ObservedRelease while omitting all chart data except metadata.
func ObserveRelease(rel *release.Release, filter ...DataFilter) ObservedRelease {
	if rel == nil {
		return ObservedRelease{}
	}

	obsRel := ObservedRelease{
		Name:      rel.Name,
		Version:   rel.Version,
		Config:    nil,
		Manifest:  rel.Manifest,
		Hooks:     nil,
		Namespace: rel.Namespace,
		Labels:    nil,
	}

	if rel.Info != nil {
		obsRel.Info = *rel.Info
	}

	if rel.Chart != nil && rel.Chart.Metadata != nil {
		if v, err := copystructure.Copy(rel.Chart.Metadata); err == nil {
			obsRel.ChartMetadata = *v.(*chart.Metadata)
		}
	}

	if len(rel.Config) > 0 {
		if v, err := copystructure.Copy(rel.Config); err == nil {
			obsRel.Config = v.(map[string]interface{})
		}
	}

	if len(rel.Hooks) > 0 {
		obsRel.Hooks = make([]release.Hook, len(rel.Hooks))
		if v, err := copystructure.Copy(rel.Hooks); err == nil {
			for i, h := range v.([]*release.Hook) {
				obsRel.Hooks[i] = *h
			}
		}
	}

	if len(rel.Labels) > 0 {
		obsRel.Labels = make(map[string]string, len(rel.Labels))
		for i, v := range rel.Labels {
			obsRel.Labels[i] = v
		}
	}

	for _, f := range filter {
		f(&obsRel)
	}

	return obsRel
}

func ObservedToInfo(rls ObservedRelease) *v2.HelmReleaseInfo {
	IgnoreHookTestEvents(&rls)
	return &v2.HelmReleaseInfo{
		Digest:        Digest(digest.SHA256, &rls).String(),
		Name:          rls.Name,
		Version:       rls.Version,
		ChartName:     rls.ChartMetadata.Name,
		ChartVersion:  rls.ChartMetadata.Version,
		ConfigDigest:  intchartutil.DigestValues(digest.SHA256, rls.Config).String(),
		FirstDeployed: rls.Info.FirstDeployed.Time,
		LastDeployed:  rls.Info.LastDeployed.Time,
		Deleted:       rls.Info.Deleted.Time,
		Status:        rls.Info.Status.String(),
	}
}
