package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sirrobot01/hearsay"
	hearsayclient "github.com/sirrobot01/hearsay/client"
	"github.com/sirrobot01/hearsay/torznab"
)

func torznabCmd() *cobra.Command {
	httpClient := &http.Client{Timeout: 2 * time.Minute}
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
			api := hearsayclient.New(hearsayd, token)
			api.HTTPClient = httpClient
			p := &torznab.Proxy{
				Upstream:      upstream,
				Client:        httpClient,
				ContextLookup: daemonLookup(api, ns, denialNS),
				Policy:        policy,
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
func daemonLookup(api *hearsayclient.Client, positiveNS, negativeNS string) torznab.ContextLookup {
	return func(ctx context.Context, subjects []string) (map[string]torznab.Claim, error) {
		paired, err := api.Evidence(ctx, positiveNS, negativeNS, subjects)
		if err != nil {
			return nil, err
		}
		answers := make(map[string]torznab.Claim, len(paired))
		for _, answer := range paired {
			var claim torznab.Claim
			switch answer.State {
			case hearsay.EvidencePositive:
				claim = toTorznabClaim(answer.Positive)
			case hearsay.EvidenceNegative:
				claim = toTorznabClaim(answer.Negative)
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

func toTorznabClaim(answer hearsayclient.Answer) torznab.Claim {
	claim := torznab.Claim{
		Subject:        answer.Subject,
		Known:          answer.Known,
		Support:        answer.Support,
		EvidenceWeight: answer.EvidenceWeight,
		Sources:        answer.Sources,
		AgeSeconds:     answer.AgeSeconds,
		Coverage:       answer.Coverage,
		Local:          answer.Local,
	}
	if value, ok := answer.BoolValue(); ok {
		claim.Value = new(value)
	}
	return claim
}
