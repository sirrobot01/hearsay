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
	var url string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status: identity, sources, and network peers",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return status(url)
		},
	}
	cmd.Flags().StringVar(&url, "url", "http://127.0.0.1:8477", "daemon base URL")
	return cmd
}

func status(base string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(strings.TrimSuffix(base, "/") + "/v1/status")
	if err != nil {
		return fmt.Errorf("cannot reach the daemon at %s — is it running? (%w)", base, err)
	}
	defer res.Body.Close()
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
	fmt.Fprintln(w, "NAMESPACE\tSOURCES WITH DATA\tKNOWN PUBLISHERS")
	for _, ns := range st.Namespaces {
		known := "-"
		if st.Transport != nil {
			known = fmt.Sprintf("%d", st.Transport.Feeds[ns.Namespace])
		}
		fmt.Fprintf(w, "%s\t%d\t%s\n", ns.Namespace, ns.Feeds, known)
	}
	fmt.Fprintln(w)
	if st.Transport == nil {
		fmt.Fprintln(w, "Network participation is disabled; local ground truth only.")
		return w.Flush()
	}
	fmt.Fprintf(w, "Swarm port\t%d\n", st.Transport.SwarmPort)
	fmt.Fprintf(w, "Gossip port\t%d\n", st.Transport.GossipPort)
	fmt.Fprintln(w)
	if len(st.Transport.Torrents) == 0 {
		fmt.Fprintln(w, "No generation blobs held yet.")
		return w.Flush()
	}
	fmt.Fprintln(w, "GENERATION BLOB\tSIZE\tHAVE\tACTIVE PEERS")
	for _, t := range st.Transport.Torrents {
		have := "0%"
		if t.Size > 0 {
			have = fmt.Sprintf("%d%%", t.Completed*100/t.Size)
		}
		fmt.Fprintf(w, "%.16s…\t%.1f KB\t%s\t%d\n", t.InfoHash, float64(t.Size)/1024, have, t.ActivePeers)
	}
	return w.Flush()
}
