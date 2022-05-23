package release

import (
	"encoding/json"

	"github.com/opencontainers/go-digest"
)

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
