package chartutil

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"helm.sh/helm/v3/pkg/chartutil"
)

const testDigestAlgo = digest.SHA256

func TestValuesDigest(t *testing.T) {
	tests := []struct {
		name   string
		values chartutil.Values
		want   digest.Digest
	}{
		{
			name:   "empty",
			values: chartutil.Values{},
			want:   "sha256:ca3d163bab055381827226140568f3bef7eaac187cebd76878e0b63e9e442356",
		},
		{
			name: "value map",
			values: chartutil.Values{
				"foo": "bar",
				"baz": map[string]string{
					"cool": "stuff",
				},
			},
			want: "sha256:3f3641788a2d4abda3534eaa90c90b54916e4c6e3a5b2e1b24758b7bfa701ecd",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DigestValues(tt.values, testDigestAlgo); got != tt.want {
				t.Errorf("DigestValues() = %v, want %v", got, tt.want)
			}
		})
	}
}
