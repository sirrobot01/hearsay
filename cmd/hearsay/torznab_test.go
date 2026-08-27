package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirrobot01/hearsay/torznab"
)

func TestDaemonLookupPreservesLocalNegative(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing token")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answers":[{"subject":"abc","state":"negative","positive":{"known":false,"value":null},"negative":{"known":true,"value":false,"support":1,"evidence_weight":1,"sources":1,"age_seconds":10,"coverage":1,"local":true}}]}`))
	}))
	defer ts.Close()

	claims, err := daemonLookup(ts.Client(), ts.URL, "token", "positive", "negative")([]string{"abc"})
	if err != nil {
		t.Fatal(err)
	}
	claim := claims["abc"]
	if claim.Cached() || !claim.Uncached() {
		t.Fatalf("local negative decoded as %+v", claim)
	}
}

func TestDefaultRankPolicy(t *testing.T) {
	policy := torznab.DefaultRankPolicy()
	if policy.Accepts(torznab.Claim{Support: 0.49, Sources: 1}) {
		t.Fatal("accepted a claim below the default threshold")
	}
	if !policy.Accepts(torznab.Claim{Support: 0.5, Sources: 1}) {
		t.Fatal("rejected a claim at the default threshold")
	}
}
