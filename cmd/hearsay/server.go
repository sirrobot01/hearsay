package main

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sirrobot01/hearsay"
	"github.com/sirrobot01/hearsay/transport"
)

type server struct {
	h     *hearsay.Hearsay
	node  *transport.Node
	token string
	hosts []string // extra Host header values to accept, from config
}

func (s *server) mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/claim", s.claim)
	mux.HandleFunc("POST /v1/claims", s.claims)
	mux.HandleFunc("POST /v1/observations", s.observations)
	mux.HandleFunc("POST /v1/outcomes", s.outcomes)
	mux.HandleFunc("GET /v1/status", s.status)
	return s.guard(mux)
}

// guard runs the checks every endpoint needs before its handler does.
// A page in the operator's browser can reach a loopback daemon: it can
// rebind DNS to defeat the origin rule, and a text/plain body makes the
// POST a simple request that skips the preflight. So the Host must name
// this daemon, a body must be real JSON, and the caller must hold the
// token.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r.Host) {
			http.Error(w, "unrecognised Host header", http.StatusMisdirectedRequest)
			return
		}
		if r.ContentLength != 0 || r.Method == http.MethodPost {
			if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || ct != "application/json" {
				http.Error(w, "want Content-Type: application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedHost accepts an IP literal or localhost, which is what a
// direct client sends, and rejects the attacker-controlled name a
// rebound lookup carries. Reverse proxies pass their own name through,
// so config lists those.
func (s *server) allowedHost(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "["), "]")
	if name == "localhost" || net.ParseIP(name) != nil {
		return true
	}
	return slices.Contains(s.hosts, name)
}

// answer is the wire form of a hint. Never a bare boolean: callers
// set their own thresholds.
type answer struct {
	Subject    string  `json:"subject,omitempty"`
	Value      any     `json:"value"`
	Confidence float64 `json:"confidence"`
	Sources    int     `json:"sources"`
	AgeSeconds int64   `json:"age_seconds"`
	Coverage   float64 `json:"coverage"`
	Local      bool    `json:"local,omitempty"`
}

func toAnswer(d hearsay.Domain, subject string, a hearsay.Answer) answer {
	out := answer{
		Subject:    subject,
		Value:      a.Value,
		Confidence: a.Confidence,
		Sources:    a.Sources,
		AgeSeconds: int64(a.Age.Seconds()),
		Coverage:   a.Coverage,
		Local:      a.Local,
	}
	if d.ValueType() == hearsay.Bool {
		out.Value = a.Value >= 0.5
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *server) claim(w http.ResponseWriter, r *http.Request) {
	ns := r.FormValue("ns")
	subject := r.FormValue("subject")
	d, ok := s.h.Domain(ns)
	if !ok {
		http.Error(w, "unknown namespace", http.StatusNotFound)
		return
	}
	a, err := s.h.Query(ns, subject)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, toAnswer(d, "", a))
}

// claims is the batch endpoint, and the one that matters: an *arr
// evaluating 30 releases issues one call.
func (s *server) claims(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NS       string   `json:"ns"`
		Subjects []string `json:"subjects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	d, ok := s.h.Domain(req.NS)
	if !ok {
		http.Error(w, "unknown namespace", http.StatusNotFound)
		return
	}
	answers := make([]answer, 0, len(req.Subjects))
	for _, subject := range req.Subjects {
		a, err := s.h.Query(req.NS, subject)
		if err != nil {
			answers = append(answers, answer{Subject: subject, Value: nil})
			continue
		}
		answers = append(answers, toAnswer(d, subject, a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ns": req.NS, "answers": answers})
}

func (s *server) observations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NS           string `json:"ns"`
		Observations []struct {
			Subject    string    `json:"subject"`
			Value      any       `json:"value"`
			ObservedAt time.Time `json:"observed_at"`
		} `json:"observations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	accepted := 0
	for _, o := range req.Observations {
		v, ok := toFloat(o.Value)
		if !ok {
			continue
		}
		at := o.ObservedAt
		if at.IsZero() {
			at = time.Now()
		}
		if s.h.Observe(req.NS, o.Subject, v, at) == nil {
			accepted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": accepted})
}

func (s *server) outcomes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NS      string `json:"ns"`
		Subject string `json:"subject"`
		Outcome any    `json:"outcome"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.h.Report(req.NS, req.Subject, req.Outcome); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	type nsStatus struct {
		Namespace string `json:"namespace"`
		Feeds     int    `json:"feeds"`
	}
	var namespaces []nsStatus
	for _, d := range s.h.Domains() {
		ns := d.Namespace()
		namespaces = append(namespaces, nsStatus{Namespace: ns, Feeds: len(s.h.Feeds(ns))})
	}
	out := map[string]any{
		"identity":   "ed25519:" + hex.EncodeToString(s.h.Identity()),
		"namespaces": namespaces,
	}
	if s.node != nil {
		out["transport"] = s.node.Stats()
	}
	writeJSON(w, http.StatusOK, out)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case float64:
		return x, true
	}
	return 0, false
}
