package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirrobot01/hearsay"
	"github.com/sirrobot01/hearsay/debrid"
)

func TestAPI(t *testing.T) {
	h, err := hearsay.New(t.TempDir(), debrid.New(debrid.RealDebrid), debrid.NewUncached(debrid.RealDebrid))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ts := httptest.NewServer((&server{h: h, token: testToken}).mux())
	defer ts.Close()

	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	body := `{"ns":"debrid.realdebrid.cached","observations":[{"subject":"` + ih + `","value":true}]}`
	res, err := do(t, ts, "POST", "/v1/observations", body)
	if err != nil {
		t.Fatal(err)
	}
	var obs map[string]int
	json.NewDecoder(res.Body).Decode(&obs)
	if obs["accepted"] != 1 {
		t.Fatalf("accepted = %d", obs["accepted"])
	}

	res, err = do(t, ts, "GET", "/v1/claim?ns=debrid.realdebrid.cached&subject="+ih, "")
	if err != nil {
		t.Fatal(err)
	}
	var a answer
	json.NewDecoder(res.Body).Decode(&a)
	if !a.Known || a.Value != true || a.Support != 1 || !a.Local {
		t.Fatalf("claim = %+v", a)
	}

	batch := `{"ns":"debrid.realdebrid.cached","subjects":["` + ih + `","ffffffffffffffffffffffffffffffffffffffff"]}`
	res, err = do(t, ts, "POST", "/v1/claims", batch)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Answers []answer `json:"answers"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	if len(out.Answers) != 2 {
		t.Fatalf("answers = %+v", out.Answers)
	}
	if out.Answers[0].Value != true || out.Answers[1].Known || out.Answers[1].Value != nil {
		t.Fatalf("answers = %+v", out.Answers)
	}

	const missing = "ffffffffffffffffffffffffffffffffffffffff"
	negative := `{"ns":"debrid.realdebrid.uncached","observations":[{"subject":"` + missing + `","value":true}]}`
	if res, err = do(t, ts, "POST", "/v1/observations", negative); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	evidenceBody := `{"positive_ns":"debrid.realdebrid.cached","negative_ns":"debrid.realdebrid.uncached","subjects":["` + ih + `","` + missing + `"]}`
	res, err = do(t, ts, "POST", "/v1/evidence", evidenceBody)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Answers []struct {
			State string `json:"state"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence.Answers) != 2 || evidence.Answers[0].State != "positive" || evidence.Answers[1].State != "negative" {
		t.Fatalf("evidence = %+v", evidence.Answers)
	}
	res.Body.Close()

	res, _ = do(t, ts, "GET", "/v1/claim?ns=nope&subject=x", "")
	if res.StatusCode != 404 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	res.Body.Close()

	res, err = do(t, ts, "GET", "/v1/status", "")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var status struct {
		Participation struct {
			Network bool `json:"network"`
		} `json:"participation"`
		Metrics hearsay.EngineMetrics `json:"metrics"`
	}
	if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Participation.Network || status.Metrics.Queries == 0 {
		t.Fatalf("status = %+v", status)
	}

	res, err = do(t, ts, "GET", "/v1/namespaces", "")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var capabilities struct {
		Protocol   string `json:"protocol"`
		Namespaces []struct {
			Namespace string `json:"namespace"`
			ValueType string `json:"value_type"`
		} `json:"namespaces"`
	}
	if err := json.NewDecoder(res.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.Protocol != hearsay.ProtocolVersion || len(capabilities.Namespaces) != 2 || capabilities.Namespaces[0].ValueType != "bool" {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}

const testToken = "3d1f0c9a2b8e4756a1c0d3f28b7e6a459c2d81f0e3b5a7c964d2081f3a6b5c7e"

// do issues an authorised, well-formed request: the checks each of
// these would otherwise trip have their own test below.
func do(t *testing.T, ts *httptest.Server, method, path, body string) (*http.Response, error) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	return ts.Client().Do(req)
}

// A page in the operator's browser must not be able to write claims.
func TestGuard(t *testing.T) {
	h, err := hearsay.New(t.TempDir(), debrid.New(debrid.RealDebrid))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ts := httptest.NewServer((&server{h: h, token: testToken, hosts: []string{"hearsay.internal"}}).mux())
	defer ts.Close()

	const body = `{"ns":"debrid.realdebrid.cached","observations":[{"subject":"2c6b6858d61da9543d4231a71db4b1c9264b0685","value":true}]}`
	for _, tc := range []struct {
		name        string
		host        string
		contentType string
		auth        string
		want        int
	}{
		{"no token", "", "application/json", "", http.StatusUnauthorized},
		{"wrong token", "", "application/json", "Bearer " + strings.Repeat("a", 64), http.StatusUnauthorized},
		{"token without scheme", "", "application/json", testToken, http.StatusUnauthorized},
		{"simple request skips preflight", "", "text/plain;charset=UTF-8", "Bearer " + testToken, http.StatusUnsupportedMediaType},
		{"form post", "", "application/x-www-form-urlencoded", "Bearer " + testToken, http.StatusUnsupportedMediaType},
		{"rebound dns", "evil.example", "application/json", "Bearer " + testToken, http.StatusMisdirectedRequest},
		{"configured proxy name", "hearsay.internal", "application/json", "Bearer " + testToken, http.StatusOK},
		{"authorised", "", "application/json", "Bearer " + testToken, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", ts.URL+"/v1/observations", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", tc.contentType)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			res, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

// A caller must not be able to date an observation into the future and
// pin it against the outcomes that are supposed to correct it.
func TestObservedAtCannotOutrankCorrections(t *testing.T) {
	h, err := hearsay.New(t.TempDir(), debrid.New(debrid.RealDebrid))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ts := httptest.NewServer((&server{h: h, token: testToken}).mux())
	defer ts.Close()

	const ns = "debrid.realdebrid.cached"
	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	poison := `{"ns":"` + ns + `","observations":[{"subject":"` + ih + `","value":true,"observed_at":"2999-01-01T00:00:00Z"}]}`
	if _, err := do(t, ts, "POST", "/v1/observations", poison); err != nil {
		t.Fatal(err)
	}

	res, err := do(t, ts, "POST", "/v1/outcomes", `{"ns":"`+ns+`","subject":"`+ih+`","outcome":false}`)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	res, err = do(t, ts, "GET", "/v1/claim?ns="+ns+"&subject="+ih, "")
	if err != nil {
		t.Fatal(err)
	}
	var a answer
	json.NewDecoder(res.Body).Decode(&a)
	if a.Value != false {
		t.Fatalf("outcome did not correct a future-dated observation: %+v", a)
	}
	if a.AgeSeconds < 0 {
		t.Fatalf("age is negative: %+v", a)
	}
}
