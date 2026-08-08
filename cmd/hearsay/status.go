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
		Transport *transport.Stats `json:"transport"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Identity\t%s\n", st.Identity)
	fmt.Fprintln(w)
	// ANNOUNCING counts peers that have the namespace enabled, which is
	// not the same as peers that have ever published in it: a default
	// install enables every adapter and announces all of them at once.
	fmt.Fprintln(w, "NAMESPACE\tSOURCES WITH DATA\tPEERS ANNOUNCING")
	for _, ns := range st.Namespaces {
		announcing := "-"
		if st.Transport != nil {
			announcing = fmt.Sprintf("%d", st.Transport.Feeds[ns.Namespace])
		}
		fmt.Fprintf(w, "%s\t%d\t%s\n", ns.Namespace, ns.Feeds, announcing)
	}
	fmt.Fprintln(w)
	if st.Transport == nil {
		fmt.Fprintln(w, "Network participation is disabled; local ground truth only.")
		return w.Flush()
	}
	fmt.Fprintf(w, "Swarm port\t%d\n", st.Transport.SwarmPort)
	fmt.Fprintf(w, "Gossip port\t%d\n", st.Transport.GossipPort)
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

	// A relay accumulates one blob per retained generation and seeds
	// them all, so the full list grows without bound and is almost
	// entirely complete and idle. Show only what is still moving.
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
