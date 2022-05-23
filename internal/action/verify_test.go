package action

import (
	"reflect"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2beta1"
	"github.com/fluxcd/helm-controller/internal/storage"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
)

func TestVerifyRelease(t *testing.T) {
	type args struct {
		config *Configuration
		obj    *helmv2.HelmRelease
		chrt   chart.Metadata
		vals   chartutil.Values
	}
	tests := []struct {
		name    string
		args    args
		want    storage.ObservedRelease
		wantErr bool
	}{
		{name: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VerifyRelease(tt.args.config, tt.args.obj, tt.args.chrt, tt.args.vals)
			if (err != nil) != tt.wantErr {
				t.Errorf("VerifyRelease() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("VerifyRelease() got = %v, want %v", got, tt.want)
			}
		})
	}
}
