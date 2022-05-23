package postrender

import (
	v2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"helm.sh/helm/v3/pkg/postrender"
	"k8s.io/api/autoscaling/v2beta1"
)

// BuildPostRenderers creates the post renderer instances from a HelmRelease
// and combines them into a single Combined post renderer.
func BuildPostRenderers(rel *v2.HelmRelease) (postrender.PostRenderer, error) {
	if rel == nil {
		return nil, nil
	}
	renderers := make([]postrender.PostRenderer, 0)
	for _, r := range rel.Spec.PostRenderers {
		if r.Kustomize != nil {
			renderers = append(renderers, NewKustomize(r.Kustomize))
		}
	}
	renderers = append(renderers, NewOriginLabels(v2beta1.SchemeGroupVersion.Group, rel.Namespace, rel.Name))
	if len(renderers) == 0 {
		return nil, nil
	}
	return NewCombined(renderers...), nil
}
