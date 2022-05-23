package chartutil

import (
	"github.com/opencontainers/go-digest"
	"helm.sh/helm/v3/pkg/chartutil"
)

// DigestValues encodes the values into the provided hash.Hash and returns
// the sum in string format, or an empty string if the encoding fails.
func DigestValues(algo digest.Algorithm, values chartutil.Values) digest.Digest {
	digester := algo.Digester()
	if err := values.Encode(digester.Hash()); err != nil {
		return ""
	}
	return digester.Digest()
}

func VerifyValues(digest digest.Digest, values chartutil.Values) bool {
	verifier := digest.Verifier()
	if err := values.Encode(verifier); err != nil {
		return false
	}
	return verifier.Verified()
}
