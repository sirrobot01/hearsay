package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sirrobot01/hearsay/torznab"
)

func torznabCmd() *cobra.Command {
	client := &http.Client{Timeout: 2 * time.Minute}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var listen, upstream, hearsayd, token, ns, denialNS string
	policy := torznab.DefaultRankPolicy()
	cmd := &cobra.Command{
		Use:   "torznab",
		Short: "Run the annotating Torznab proxy between an *arr and its indexer",
		Long: "Forwards every request upstream unchanged. On search results it adds\n" +
			"hearsay evidence attributes to each release and reorders claimed-cached\n" +
			"releases first. On any failure the upstream result passes through untouched.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			var err error
			if policy.MinSupport < 0 || policy.MinSupport > 1 {
				return fmt.Errorf("min support must be between 0 and 1")
			}
			if policy.MinEvidence < 0 {
				return fmt.Errorf("min evidence must not be negative")
			}
			if policy.MinSources < 1 {
				return fmt.Errorf("min sources must be at least 1")
			}
			if policy.MaxAge <= 0 {
				return fmt.Errorf("max age must be positive")
			}
			if token, err = clientToken(token); err != nil {
				return err
			}
			hearsayd = strings.TrimSuffix(hearsayd, "/")
			log.Info("proxying", "listen", listen, "upstream", upstream, "ns", ns)
			p := &torznab.Proxy{
				Upstream: upstream,
				Client:   client,
				Lookup:   daemonLookup(client, hearsayd, token, ns, denialNS),
				Policy:   policy,
			}
			return http.ListenAndServe(listen, p)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8478", "listen address")
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream Torznab base URL, e.g. http://prowlarr:9696")
	cmd.Flags().StringVar(&hearsayd, "hearsayd", "http://127.0.0.1:8477", "hearsay daemon base URL")
	cmd.Flags().StringVar(&token, "token", "", "daemon API token (default: read from the daemon's data directory, or $HEARSAY_TOKEN)")
	cmd.Flags().StringVar(&ns, "ns", "debrid.realdebrid.cached", "namespace to query")
	cmd.Flags().StringVar(&denialNS, "denial-ns", "debrid.realdebrid.uncached", "complementary denial namespace")
	cmd.Flags().Float64Var(&policy.MinSupport, "min-support", policy.MinSupport, "minimum support required to reorder a release")
	cmd.Flags().Float64Var(&policy.MinEvidence, "min-evidence", policy.MinEvidence, "minimum absolute evidence weight required to reorder a release")
	cmd.Flags().IntVar(&policy.MinSources, "min-sources", policy.MinSources, "minimum claiming sources required to reorder a release")
	cmd.Flags().DurationVar(&policy.MaxAge, "max-age", policy.MaxAge, "oldest claim allowed to reorder a release")
	cmd.MarkFlagRequired("upstream")
	return cmd
}

// daemonLookup resolves complementary namespaces through the batch evidence API.
func daemonLookup(client *http.Client, hearsayd, token, positiveNS, negativeNS string) torznab.Lookup {
	return func(subjects []string) (map[string]torznab.Claim, error) {
		reqBody, err := json.Marshal(map[string]any{
			"positive_ns": positiveNS,
			"negative_ns": negativeNS,
			"subjects":    subjects,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, hearsayd+"/v1/evidence", bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("hearsayd: %s", res.Status)
		}
		var out struct {
			Answers []struct {
				Subject  string        `json:"subject"`
				State    string        `json:"state"`
				Positive torznab.Claim `json:"positive"`
				Negative torznab.Claim `json:"negative"`
			} `json:"answers"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			return nil, err
		}
		answers := make(map[string]torznab.Claim, len(out.Answers))
		for _, answer := range out.Answers {
			var claim torznab.Claim
			switch answer.State {
			case "positive":
				claim = answer.Positive
			case "negative":
				claim = answer.Negative
				claim.Denied = true
			default:
				claim.Coverage = max(answer.Positive.Coverage, answer.Negative.Coverage)
			}
			claim.Subject = answer.Subject
			answers[answer.Subject] = claim
		}
		return answers, nil
	}
}
