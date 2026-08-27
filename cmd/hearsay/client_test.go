package main

import (
	"net/http/httptest"
	"testing"

	"github.com/sirrobot01/hearsay"
	hearsayclient "github.com/sirrobot01/hearsay/client"
	"github.com/sirrobot01/hearsay/debrid"
)

func TestDaemonClientWorkflow(t *testing.T) {
	positiveNS := debrid.New(debrid.RealDebrid).Namespace()
	negativeNS := debrid.NewUncached(debrid.RealDebrid).Namespace()
	engine, err := hearsay.New(t.TempDir(), debrid.New(debrid.RealDebrid), debrid.NewUncached(debrid.RealDebrid))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	daemon := httptest.NewServer((&server{h: engine, token: testToken}).mux())
	defer daemon.Close()

	api := hearsayclient.New(daemon.URL, testToken)
	api.HTTPClient = daemon.Client()
	const infohash = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	accepted, err := api.Observe(t.Context(), positiveNS, []hearsayclient.Observation{{
		Subject: infohash,
		Value:   true,
	}})
	if err != nil || accepted != 1 {
		t.Fatalf("accepted = %d, %v", accepted, err)
	}
	answers, err := api.Evidence(t.Context(), positiveNS, negativeNS, []string{infohash})
	if err != nil || len(answers) != 1 || answers[0].State != hearsay.EvidencePositive {
		t.Fatalf("evidence = %+v, %v", answers, err)
	}
	if err := api.Report(t.Context(), positiveNS, infohash, false); err != nil {
		t.Fatal(err)
	}
	answers, err = api.Evidence(t.Context(), positiveNS, negativeNS, []string{infohash})
	if err != nil || answers[0].State != hearsay.EvidenceNegative {
		t.Fatalf("corrected evidence = %+v, %v", answers, err)
	}
	protocol, namespaces, err := api.Namespaces(t.Context())
	if err != nil || protocol != hearsay.ProtocolVersion || len(namespaces) != 2 {
		t.Fatalf("capabilities = %q, %+v, %v", protocol, namespaces, err)
	}
}
