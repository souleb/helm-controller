package chartutil

import (
	intcmp "github.com/fluxcd/helm-controller/internal/cmp"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/google/go-cmp/cmp"
	"helm.sh/helm/v3/pkg/chart"
)

type reporter = intcmp.SimpleReporter

// DiffMeta returns if the two chart.Metadata differ.
func DiffMeta(x, y chart.Metadata) (diff string, eq bool) {
	r := new(reporter)
	if diff := cmp.Diff(x, y, cmp.Reporter(r)); diff != "" {
		return r.String(), false
	}
	return "", true
}

// DiffSpec returns if the two v1beta1.HelmChartSpec differ.
func DiffSpec(x, y sourcev1.HelmChartSpec) (diff string, eq bool) {
	r := new(reporter)
	if diff := cmp.Diff(x, y, cmp.Reporter(r)); diff != "" {
		return r.String(), false
	}
	return "", true
}
