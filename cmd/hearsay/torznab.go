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
	var listen, upstream, hearsayd, token, ns string
	cmd := &cobra.Command{
		Use:   "torznab",
		Short: "Run the annotating Torznab proxy between an *arr and its indexer",
		Long: "Forwards every request upstream unchanged. On search results it adds\n" +
			"hearsay confidence attributes to each release and reorders claimed-cached\n" +
			"releases first. On any failure the upstream result passes through untouched.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			var err error
			if token, err = clientToken(token); err != nil {
				return err
			}
			hearsayd = strings.TrimSuffix(hearsayd, "/")
			log.Info("proxying", "listen", listen, "upstream", upstream, "ns", ns)
			p := &torznab.Proxy{
				Upstream: upstream,
				Client:   client,
				Lookup:   daemonLookup(client, hearsayd, token, ns),
			}
			return http.ListenAndServe(listen, p)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8478", "listen address")
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream Torznab base URL, e.g. http://prowlarr:9696")
	cmd.Flags().StringVar(&hearsayd, "hearsayd", "http://127.0.0.1:8477", "hearsay daemon base URL")
	cmd.Flags().StringVar(&token, "token", "", "daemon API token (default: read from the daemon's data directory, or $HEARSAY_TOKEN)")
	cmd.Flags().StringVar(&ns, "ns", "debrid.realdebrid.cached", "namespace to query")
	cmd.MarkFlagRequired("upstream")
	return cmd
}

// daemonLookup answers subjects through the daemon's batch claims
// endpoint. Daemon answers carry positive evidence only, which is the
// contract torznab.Lookup requires.
func daemonLookup(client *http.Client, hearsayd, token, ns string) torznab.Lookup {
	return func(subjects []string) (map[string]torznab.Claim, error) {
		reqBody, err := json.Marshal(map[string]any{"ns": ns, "subjects": subjects})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, hearsayd+"/v1/claims", bytes.NewReader(reqBody))
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
			Answers []torznab.Claim `json:"answers"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			return nil, err
		}
		answers := make(map[string]torznab.Claim, len(out.Answers))
		for _, a := range out.Answers {
			answers[a.Subject] = a
		}
		return answers, nil
	}
}
