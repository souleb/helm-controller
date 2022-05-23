package storage

import (
	"encoding/json"

	"github.com/mitchellh/copystructure"
	"github.com/opencontainers/go-digest"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
)

// ObservedRelease is a copy of a release.Release as observed to be written to
// a Helm storage driver by an Observator. The object is detached from the Helm
// storage object, and mutations to it do not change the underlying release
// object.
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

	// digest contains the checksum of the ObservedRelease JSON data.
	// It can be accessed using Digest, and is normally lazy-loaded during
	// retrieval of the object from the Observator.
	digest digest.Digest
}

// NewObservedRelease deep copies the values from the provided release.Release
// into a new ObservedRelease while omitting all chart data except metadata.
func NewObservedRelease(rel *release.Release) ObservedRelease {
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
	return obsRel
}

// Digest calculates the digest of the given ObservedRelease by JSON encoding
// it into a hash.Hash of the given digest.Algorithm. The algorithm is expected
// to have been confirmed to be available by the caller, not doing this may
// result in panics.
func Digest(algo digest.Algorithm, rel *ObservedRelease) digest.Digest {
	if rel == nil {
		return ""
	}
	digester := algo.Digester()
	enc := json.NewEncoder(digester.Hash())
	if err := enc.Encode(rel); err != nil {
		return ""
	}
	return digester.Digest()
}

func VerifyDigest(digest digest.Digest, rel *ObservedRelease) bool {
	verifier := digest.Verifier()
	enc := json.NewEncoder(verifier)
	if err := enc.Encode(rel); err != nil {
		return false
	}
	return verifier.Verified()
}

// Digest of the ObservedRelease. If empty, it can be calculated using
// Digest. It is normally expected to be lazy-loaded by the Observator
// methods.
func (in ObservedRelease) Digest() digest.Digest {
	return in.digest
}

func (in ObservedRelease) Validate(digest digest.Digest) {
	digest.Verifier()
}

func (in ObservedRelease) HasStatus(status release.Status) bool {
	return in.Info.Status == status
}

func (in ObservedRelease) GetTestHooks() []release.Hook {
	var res []release.Hook
	for _, h := range in.Hooks {
		for _, e := range h.Events {
			if e == release.HookTest {
				res = append(res, h)
			}
		}
	}
	return res
}

func (in ObservedRelease) HasBeenTested() bool {
	for _, h := range in.Hooks {
		for _, e := range h.Events {
			if e == release.HookTest {
				if !h.LastRun.StartedAt.IsZero() {
					return true
				}
			}
		}
	}
	return false
}

func (in ObservedRelease) HasFailedTests() bool {
	for _, h := range in.Hooks {
		for _, e := range h.Events {
			if e == release.HookTest {
				if h.LastRun.Phase == release.HookPhaseFailed {
					return true
				}
			}
		}
	}
	return false
}

// DeepCopy deep copies the ObservedRelease, creating a new ObservedRelease.
func (in ObservedRelease) DeepCopy() ObservedRelease {
	out := ObservedRelease{}
	in.DeepCopyInto(&out)
	return out
}

// DeepCopyInto deep copies the ObservedRelease, writing it into out.
func (in ObservedRelease) DeepCopyInto(out *ObservedRelease) {
	if out == nil {
		return
	}

	out.Name = in.Name
	out.Version = in.Version
	out.Info = in.Info
	out.Manifest = in.Manifest
	out.Namespace = in.Namespace

	if v, err := copystructure.Copy(in.ChartMetadata); err == nil {
		out.ChartMetadata = v.(chart.Metadata)
	}

	if v, err := copystructure.Copy(in.Config); err == nil {
		out.Config = v.(map[string]interface{})
	}

	if len(in.Hooks) > 0 {
		out.Hooks = make([]release.Hook, len(in.Hooks))
		if v, err := copystructure.Copy(in.Hooks); err == nil {
			for i, h := range v.([]release.Hook) {
				out.Hooks[i] = h
			}
		}
	}

	if len(in.Labels) > 0 {
		out.Labels = make(map[string]string, len(in.Labels))
		for i, v := range in.Labels {
			out.Labels[i] = v
		}
	}
}
