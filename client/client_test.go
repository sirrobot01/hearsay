package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirrobot01/hearsay"
)

func TestClientWorkflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/namespaces":
			json.NewEncoder(w).Encode(map[string]any{
				"protocol": hearsay.ProtocolVersion,
				"namespaces": []Namespace{{
					Name: "ns.bool", ValueType: "bool", FreshnessSeconds: 3600,
				}},
			})
		case "/v1/claims":
			json.NewEncoder(w).Encode(map[string]any{
				"answers": []Answer{{Subject: "abc", Known: true, Value: true, Support: 1}},
			})
		case "/v1/evidence":
			json.NewEncoder(w).Encode(map[string]any{
				"answers": []PairedAnswer{{
					Subject: "abc", State: hearsay.EvidencePositive,
					Positive: Answer{Known: true, Value: true, EvidenceWeight: 0.8},
				}},
			})
		case "/v1/observations":
			json.NewEncoder(w).Encode(map[string]int{"accepted": 1})
		case "/v1/outcomes":
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL, "token")
	protocol, namespaces, err := client.Namespaces(t.Context())
	if err != nil || protocol != hearsay.ProtocolVersion || len(namespaces) != 1 {
		t.Fatalf("namespaces = %q, %+v, %v", protocol, namespaces, err)
	}
	claims, err := client.Claims(t.Context(), "ns.bool", []string{"abc"})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	if value, ok := claims[0].BoolValue(); !ok || !value {
		t.Fatalf("claim value = %v, %v", value, ok)
	}
	evidence, err := client.Evidence(t.Context(), "ns.positive", "ns.negative", []string{"abc"})
	if err != nil || len(evidence) != 1 || evidence[0].State != hearsay.EvidencePositive {
		t.Fatalf("evidence = %+v, %v", evidence, err)
	}
	accepted, err := client.Observe(t.Context(), "ns.bool", []Observation{{Subject: "abc", Value: true}})
	if err != nil || accepted != 1 {
		t.Fatalf("accepted = %d, %v", accepted, err)
	}
	if err := client.Report(t.Context(), "ns.bool", "abc", true); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "bad subject"})
	}))
	defer server.Close()

	_, err := New(server.URL, "token").Claims(t.Context(), "ns.bool", []string{"bad"})
	httpError, ok := errors.AsType[*HTTPError](err)
	if !ok || httpError.StatusCode != http.StatusBadRequest || httpError.Message != "bad subject" {
		t.Fatalf("error = %#v", err)
	}
}

func TestRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := New("http://127.0.0.1:1", "token").Claims(ctx, "ns.bool", []string{"abc"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
