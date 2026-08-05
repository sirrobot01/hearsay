package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirrobot01/hearsay"
	"github.com/sirrobot01/hearsay/debrid"
)

func TestAPI(t *testing.T) {
	h, err := hearsay.New(t.TempDir(), debrid.New(debrid.RealDebrid))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ts := httptest.NewServer((&server{h: h}).mux())
	defer ts.Close()

	const ih = "2c6b6858d61da9543d4231a71db4b1c9264b0685"
	body := `{"ns":"debrid.realdebrid.cached","observations":[{"subject":"` + ih + `","value":true}]}`
	res, err := ts.Client().Post(ts.URL+"/v1/observations", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var obs map[string]int
	json.NewDecoder(res.Body).Decode(&obs)
	if obs["accepted"] != 1 {
		t.Fatalf("accepted = %d", obs["accepted"])
	}

	res, err = ts.Client().Get(ts.URL + "/v1/claim?ns=debrid.realdebrid.cached&subject=" + ih)
	if err != nil {
		t.Fatal(err)
	}
	var a answer
	json.NewDecoder(res.Body).Decode(&a)
	if a.Value != true || a.Confidence != 1 || !a.Local {
		t.Fatalf("claim = %+v", a)
	}

	batch := `{"ns":"debrid.realdebrid.cached","subjects":["` + ih + `","ffffffffffffffffffffffffffffffffffffffff"]}`
	res, err = ts.Client().Post(ts.URL+"/v1/claims", "application/json", strings.NewReader(batch))
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
	if out.Answers[0].Value != true || out.Answers[1].Value != false {
		t.Fatalf("answers = %+v", out.Answers)
	}

	res, _ = ts.Client().Get(ts.URL + "/v1/claim?ns=nope&subject=x")
	if res.StatusCode != 404 {
		t.Fatalf("status = %d", res.StatusCode)
	}
}
