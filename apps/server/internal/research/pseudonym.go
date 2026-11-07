package research

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
)

// Pseudonymize replaces athlete keys with a deterministic keyed pseudonym and
// returns the mapping separately. The mapping is sensitive governance data and
// must never be written into an aggregate research report.
func Pseudonymize(snapshot ProspectiveSnapshot, outcomes []Outcome, withdraw []string, secret []byte) (ProspectiveSnapshot, []Outcome, []string, map[string]string, error) {
	if len(secret) < 16 {
		return ProspectiveSnapshot{}, nil, nil, nil, errors.New("pseudonym secret must be at least 16 bytes")
	}
	mapping := make(map[string]string)
	pseudonym := func(key string) string {
		if key == "" {
			return ""
		}
		if v, ok := mapping[key]; ok {
			return v
		}
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte("rai.research.athlete/v1\x00"))
		_, _ = mac.Write([]byte(key))
		v := "ath_" + hex.EncodeToString(mac.Sum(nil)[:16])
		mapping[key] = v
		return v
	}
	out := snapshot
	out.Observations = append([]Prediction(nil), snapshot.Observations...)
	for i := range out.Observations {
		out.Observations[i].AthleteKey = pseudonym(out.Observations[i].AthleteKey)
	}
	outcomesOut := append([]Outcome(nil), outcomes...)
	for i := range outcomesOut {
		outcomesOut[i].AthleteKey = pseudonym(outcomesOut[i].AthleteKey)
	}
	withdrawOut := make([]string, len(withdraw))
	for i, key := range withdraw {
		withdrawOut[i] = pseudonym(key)
	}
	return out, outcomesOut, withdrawOut, mapping, nil
}

// SortedMapping returns the mapping as deterministic key/value pairs.
func SortedMapping(mapping map[string]string) [][2]string {
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, mapping[k]})
	}
	return out
}
