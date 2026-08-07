package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func torznabCmd() *cobra.Command {
	p := &proxy{
		client: &http.Client{Timeout: 2 * time.Minute},
		log:    slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	var listen string
	cmd := &cobra.Command{
		Use:   "torznab",
		Short: "Run the annotating Torznab proxy between an *arr and its indexer",
		Long: "Forwards every request upstream unchanged. On search results it adds\n" +
			"hearsay confidence attributes to each release and reorders claimed-cached\n" +
			"releases first. On any failure the upstream result passes through untouched.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			var err error
			if p.token, err = clientToken(p.token); err != nil {
				return err
			}
			p.upstream = strings.TrimSuffix(p.upstream, "/")
			p.hearsayd = strings.TrimSuffix(p.hearsayd, "/")
			p.log.Info("proxying", "listen", listen, "upstream", p.upstream, "ns", p.ns)
			return http.ListenAndServe(listen, p)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8478", "listen address")
	cmd.Flags().StringVar(&p.upstream, "upstream", "", "upstream Torznab base URL, e.g. http://prowlarr:9696")
	cmd.Flags().StringVar(&p.hearsayd, "hearsayd", "http://127.0.0.1:8477", "hearsay daemon base URL")
	cmd.Flags().StringVar(&p.token, "token", "", "daemon API token (default: read from the daemon's data directory, or $HEARSAY_TOKEN)")
	cmd.Flags().StringVar(&p.ns, "ns", "debrid.realdebrid.cached", "namespace to query")
	cmd.MarkFlagRequired("upstream")
	return cmd
}

var searchTypes = map[string]bool{
	"search": true, "tvsearch": true, "movie": true, "music": true, "book": true,
}

type proxy struct {
	upstream string
	hearsayd string
	token    string
	ns       string
	client   *http.Client
	log      *slog.Logger
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := p.upstream + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	req.Header.Del("Accept-Encoding")
	res, err := p.client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK || !searchTypes[r.URL.Query().Get("t")] {
		copyResponse(w, res)
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := rewrite(body, p.lookup)
	copyHeaders(w, res)
	w.WriteHeader(res.StatusCode)
	w.Write(out)
}

func (p *proxy) lookup(subjects []string) (map[string]claim, error) {
	reqBody, err := json.Marshal(map[string]any{"ns": p.ns, "subjects": subjects})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, p.hearsayd+"/v1/claims", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hearsayd: %s", res.Status)
	}
	var out struct {
		Answers []claim `json:"answers"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	answers := make(map[string]claim, len(out.Answers))
	for _, a := range out.Answers {
		answers[a.Subject] = a
	}
	return answers, nil
}

func copyHeaders(w http.ResponseWriter, res *http.Response) {
	for k, vs := range res.Header {
		if k == "Content-Length" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

func copyResponse(w http.ResponseWriter, res *http.Response) {
	copyHeaders(w, res)
	w.WriteHeader(res.StatusCode)
	io.Copy(w, res.Body)
}
