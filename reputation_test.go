package hearsay

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestUnverifiedInfluenceIsCapped(t *testing.T) {
	reputation, err := openReputation(filepath.Join(t.TempDir(), "reputation.json"))
	if err != nil {
		t.Fatal(err)
	}
	feeds := make([]string, 20)
	for i := range feeds {
		feeds[i] = fmt.Sprintf("feed-%d", i)
	}
	weights := reputation.weights(feeds)
	var total float64
	for _, weight := range weights {
		total += weight
	}
	if total > unverifiedPoolWeight+1e-9 {
		t.Fatalf("unverified weight = %v", total)
	}
}

func TestReputationMigratesLegacyWeights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reputation.json")
	if err := os.WriteFile(path, []byte(`{"trusted":0.9,"weak":0.1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reputation, err := openReputation(path)
	if err != nil {
		t.Fatal(err)
	}
	if reputation.byID["trusted"].samples() != legacySamples || reputation.weight("trusted") <= reputation.weight("weak") {
		t.Fatalf("legacy migration = %+v", reputation.byID)
	}
}
