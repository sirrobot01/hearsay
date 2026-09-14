package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/sirrobot01/hearsay"
	"github.com/sirrobot01/hearsay/transport"
)

func statusCmd() *cobra.Command {
	var url, token string
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status: identity, sources, and network peers",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			token, err := clientToken(token)
			if err != nil {
				return err
			}
			return status(url, token, all)
		},
	}
	cmd.Flags().StringVar(&url, "url", "http://127.0.0.1:8477", "daemon base URL")
	cmd.Flags().StringVar(&token, "token", "", "API token (default: read from the daemon's data directory, or $HEARSAY_TOKEN)")
	cmd.Flags().BoolVar(&all, "all", false, "list every generation blob, not only the incomplete or active ones")
	return cmd
}

func status(base, token string, all bool) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(base, "/")+"/v1/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the daemon at %s — is it running? (%w)", base, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("daemon rejected the API token; pass --token or set HEARSAY_TOKEN")
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned %s", res.Status)
	}
	var st struct {
		Identity   string `json:"identity"`
		Namespaces []struct {
			Namespace string `json:"namespace"`
			Feeds     int    `json:"feeds"`
		} `json:"namespaces"`
		Participation struct {
			Network    bool `json:"network"`
			Publishing bool `json:"publishing"`
		} `json:"participation"`
		Metrics   hearsay.EngineMetrics `json:"metrics"`
		Transport *transport.Stats      `json:"transport"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Identity\t%s\n", st.Identity)
	mode := "local only"
	if st.Participation.Network {
		mode = "network reader and relay"
	}
	if st.Participation.Publishing {
		mode = "network publisher and relay"
	}
	fmt.Fprintf(w, "Mode\t%s\n", mode)
	fmt.Fprintf(w, "Activity\t%d observations, %d queries, %d outcomes\n",
		st.Metrics.Observations, st.Metrics.Queries, st.Metrics.Outcomes)
	fmt.Fprintln(w)
	if len(st.Namespaces) == 0 {
		fmt.Fprintln(w, "No namespaces configured.")
	} else {
		fmt.Fprintln(w, "NAMESPACE\tSOURCES\tQUERIES\tKNOWN\tOUTCOMES\tSOURCE ACCURACY\tPEERS")
	}
	for _, ns := range st.Namespaces {
		metrics := st.Metrics.Namespaces[ns.Namespace]
		announcing := "-"
		if st.Transport != nil {
			announcing = fmt.Sprintf("%d", st.Transport.Feeds[ns.Namespace])
		}
		known := "-"
		if metrics.Queries > 0 {
			known = fmt.Sprintf("%.1f%%", 100*metrics.KnownRate())
		}
		accuracy := "-"
		if metrics.VerifiedClaims > 0 {
			accuracy = fmt.Sprintf("%.1f%%", 100*metrics.SourceAccuracy())
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%d\t%s\t%s\n",
			ns.Namespace, ns.Feeds, metrics.Queries, known, metrics.Outcomes, accuracy, announcing)
	}
	fmt.Fprintln(w)
	if st.Transport == nil {
		fmt.Fprintln(w, "Network participation is disabled; local ground truth only.")
		return w.Flush()
	}
	fmt.Fprintf(w, "Swarm port\t%d\n", st.Transport.SwarmPort)
	fmt.Fprintf(w, "Gossip port\t%d\n", st.Transport.GossipPort)
	fmt.Fprintf(w, "Swarm storage\t%s / %s\n", humanSize(st.Transport.StoredBytes), humanSize(st.Transport.MaxStorage))
	fmt.Fprintf(w, "Retained swarm limit\t%d\n", st.Transport.MaxSeededTorrents)
	var retrying, quarantined int
	for _, count := range st.Transport.RetryingFeeds {
		retrying += count
	}
	for _, count := range st.Transport.QuarantinedFeeds {
		quarantined += count
	}
	fmt.Fprintf(w, "Feed health\t%d retrying, %d quarantined, %d max per namespace\n",
		retrying, quarantined, st.Transport.MaxFeedsPerNamespace)
	fmt.Fprintln(w)
	blobs := st.Transport.Torrents
	if len(blobs) == 0 {
		fmt.Fprintln(w, "No generation blobs held yet.")
		return w.Flush()
	}
	var size, completed int64
	var peers, partial int
	for _, t := range blobs {
		size += t.Size
		completed += t.Completed
		peers += t.ActivePeers
		if t.Completed < t.Size {
			partial++
		}
	}
	fmt.Fprintf(w, "Generation blobs\t%d held, %s, %d incomplete, %d active peers\n",
		len(blobs), humanSize(size), partial, peers)

	// Most retained blobs are complete and idle, so show only what is moving.
	shown := blobs
	if !all {
		shown = nil
		for _, t := range blobs {
			if t.ActivePeers > 0 || t.Completed < t.Size {
				shown = append(shown, t)
			}
		}
		if len(shown) == 0 {
			fmt.Fprintln(w, "\tall complete and idle; --all lists them")
			return w.Flush()
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "GENERATION BLOB\tSIZE\tHAVE\tACTIVE PEERS")
	for _, t := range shown {
		have := "0%"
		if t.Size > 0 {
			have = fmt.Sprintf("%d%%", t.Completed*100/t.Size)
		}
		fmt.Fprintf(w, "%.16s…\t%s\t%s\t%d\n", t.InfoHash, humanSize(t.Size), have, t.ActivePeers)
	}
	return w.Flush()
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
