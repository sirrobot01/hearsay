# Hearsay

Hearsay shares observations about external state between independent operators. The observations are byproducts of work the operators do anyway.

Hearsay is a hint layer, not an authority. An answer tells you what other operators believe, how strongly, and how recently. It does not tell you what is true.

## How it works

1. Your tools do normal work and learn facts. Example: an add to a debrid service shows if a torrent is cached.
2. Hearsay records these facts as local ground truth.
3. Hearsay publishes signed snapshots of your truth, called generations. Other operators fetch yours. You fetch theirs.
4. A query returns whether evidence exists, its value, support, weight, and age. Fresh local truth wins.
5. You act on a hint. The outcome reveals the truth. Hearsay adjusts each source's weight from this feedback.

A wrong source loses weight automatically. There is no voting, no global reputation, and no moderation.

**The publication rule:** publish only what you observed as a byproduct of real work. Never probe. The `Domain` interface has no probe method by design.

## Install

On Linux or macOS, run the install script:

```
curl -fsSL https://hearsay.decypharr.com/install.sh | sh
```

The script installs the correct binary for your system, verifies its checksum, and registers a service (systemd on Linux, launchd on macOS). The standalone service joins the network and publishes observations in every built-in namespace by default.

On Windows, download the zip from the [releases page](https://github.com/sirrobot01/hearsay/releases). Releases cover Linux, macOS, and Windows, on amd64 and arm64, with a `checksums.txt`.

To build from source instead:

```
go build ./cmd/hearsay
```

## Run the daemon

```
hearsay serve
```

With no configuration, the standalone daemon joins the network, publishes observations, and enables every built-in adapter. These defaults preserve the behavior of existing installations across upgrades. Create a configuration file to narrow the active namespaces or opt out:

```yaml
# Loaded from <user config dir>/hearsay/config.yaml if present,
# e.g. ~/.config/hearsay/config.yaml. Or: hearsay serve --config <path>
dir: /var/lib/hearsay
listen: "127.0.0.1:8477"
allowed_hosts: []      # extra Host header values; IPs and localhost always pass
hearsay:
  participate: true    # join the DHT, fetch, and relay
  publish: true        # share your observations
  interval: "30m"      # publish and fetch cycle
  port: 0              # swarm listen port; 0 = random
  gossip_port: 0       # discovery listen port; 0 = random
  max_storage_bytes: 1073741824 # cap relayed generation blobs at 1 GiB
  max_feeds_per_namespace: 256  # bound discovered publisher metadata
  follow: []           # empty = discover; non-empty = publisher allowlist
adapters:
  debrid: [realdebrid] # activates cached and uncached namespaces
  usenet: []
  torrent: false       # activates public-torrent availability
```

Omitting `participate` or `publish` keeps it enabled. Omitting an adapter list enables every built-in value in that category; use `[]` to disable the category. The torrent adapter also defaults on.

`participate: true, publish: false` is reader/relay mode: your IP joins the selected public namespace topics, fetched generations are relayed, and your observations remain local. Set `participate: false` for local-only mode. The legacy `enabled` key remains accepted as a participation override.

`hearsay status` shows the effective participation mode, active namespaces, query coverage, verified source accuracy, retry/quarantine counts, and bounded swarm storage. Engine counters persist locally across restarts.

Built-in adapters are debrid cache/uncached evidence by provider, usenet completeness by backbone, and `torrent.public.availability` for terminal public-torrent completion ratios. The torrent adapter accepts only outcomes from real terminal attempts; never feed it live samples, cancellations, or probes.

## HTTP API

```
GET  /v1/claim?ns=debrid.realdebrid.cached&subject=<infohash>
POST /v1/claims        {"ns": "...", "subjects": ["...", "..."]}   # batch; the primary path
POST /v1/evidence      {"positive_ns": "...cached", "negative_ns": "...uncached", "subjects": ["..."]}
POST /v1/observations  {"ns": "...", "observations": [{"subject": "...", "value": true}]}
POST /v1/outcomes      {"ns": "...", "subject": "...", "outcome": false}
GET  /v1/namespaces
GET  /v1/status
```

Every endpoint needs the API token. The daemon writes the token to `<dir>/token` on the first run and prints the path at startup. Send it as a bearer token, and send JSON bodies as `application/json`:

```
curl -H "Authorization: Bearer $(cat ~/.config/hearsay/token)" \
     127.0.0.1:8477/v1/status
```

The token is necessary even on loopback. A web page in your browser can post to `127.0.0.1` without it being your own request. The `hearsay` commands read the token file for you. Use `--token` or `HEARSAY_TOKEN` to reach a daemon on another machine.

An answer distinguishes unknown from false. `support` is the share of active source weight backing the value; `evidence_weight` is the absolute backing mass:

```json
{"known": true, "value": true, "support": 0.92, "evidence_weight": 1.74, "sources": 4, "age_seconds": 21600, "coverage": 0.81}
```

`/v1/evidence` combines complementary Boolean namespaces into `positive`, `negative`, or `unknown` without collapsing a miss into a denial.

Push observations from your own work. Report outcomes after you act on a hint: this corrects the source weights.

## Torznab proxy

```
hearsay torznab --upstream http://prowlarr:9696 \
  --min-support 0.5 --min-evidence 0.3 --min-sources 1 --max-age 24h
```

Point your *arr at the proxy instead of the indexer. The proxy adds `hearsay_state`, `hearsay_support`, `hearsay_evidence_weight`, and—when known—`hearsay_cached` attributes. It reorders evidence that passes the configured policy. Fresh local observations always qualify. On any failure, it returns the upstream result untouched.

The proxy is also a library, `github.com/sirrobot01/hearsay/torznab`. An application that embeds the engine can serve the same proxy from its own process, answered by an in-process `Lookup` instead of the daemon.

## Embed the library

```go
h, _ := hearsay.New(dir, debrid.New(debrid.RealDebrid), debrid.NewUncached(debrid.RealDebrid))
h.Observe("debrid.realdebrid.cached", infohash, 1, time.Now()) // your work learned this
a, _ := h.Query("debrid.realdebrid.cached", infohash)          // ask before you act
h.Report("debrid.realdebrid.cached", infohash, wasInstant)     // the outcome corrects weights
```

Add the `transport.Syncer` to participate in the network. See `cmd/hearsay` for the wiring.

For daemon-backed hosts, use the typed [`client`](./client) package and the [integration guide](./docs/integration.md). Cross-implementation details are in the [HSY2 protocol specification](./docs/protocol.md).

For a complete debrid loop, start an advisor in shadow mode, preserve normal ordering, and report the chosen add outcome:

```go
advisor, _ := debrid.NewAdvisor(h, debrid.RealDebrid, debrid.Shadow, metricsPath)
decisions, _ := advisor.EvaluateBatch(infohashes)

chosen := decisions[selected]
advisor.RecordOutcome(chosen.ID, wasInstant)
for i, decision := range decisions {
    if i != selected {
        advisor.Discard(decision.ID)
    }
}
```

Shadow mode measures coverage, verified accuracy, and potentially avoidable uncached additions without changing behavior. Active mode sets `Decision.UseHint` for known positive or negative evidence. Metrics remain local and persist when a metrics path is supplied.

## Add a domain

Implement the `Domain` interface. A domain must satisfy all four criteria:

1. The state is expensive or impossible to query at scale.
2. Many independent parties observe it as a byproduct of normal work.
3. A wrong answer is cheap to act on, and the action reveals the truth.
4. Subjects have stable identifiers that each party can compute alone.

## Privacy

- Hearsay publishes only what you observed through real work, never what you searched for. A failure is an observation too: an incomplete usenet download, or a torrent add that was not served from cache, publishes a claim — and that claim tells the network you attempted the subject. Disable `publish` to contribute nothing.
- Bloom filters are testable, not enumerable. An adversary cannot list your subjects, but can test one they already hold.
- Scalar generations expose derived subject keys, values, and observation times. The original subject is not present, but an adversary can test a candidate they already know. Usenet completeness and public-torrent availability use scalar payloads.
- Each generation carries a fresh signing key. Your long-term identity signs that key, and the identity is in every generation. The identity is also the key that others follow you by. All of your generations therefore stay linkable to each other and to you. The fresh key does not prevent this. It only limits what a leaked signing key can forge.
- DHT topics are public: participation puts your IP on a per-namespace list. The standalone daemon participates and publishes by default; set `participate: false` to stay local-only or `publish: false` to remain a reader and relay.

## Protocol notes

- A claim is `(namespace, subject, value, observed_at)` — a claim, not a fact.
- Bloom payloads carry positive claims only, so a debrid denial travels as a positive claim in the paired namespace `debrid.<vendor>.uncached`: membership means "added and found not cached". Denials decay with a 6 hour half-life — anyone's uncached download can flip the state — where cached claims get 24 hours.
- `HSY2` generations are immutable and signed. Boolean namespaces use time-bucketed Bloom filters; scalar records carry their observation time. Domain freshness policies expire local and remote evidence without refreshing old claims when a generation is rebuilt.
- Consumers keep the last 8 generations per feed for delivery resilience. Claim age comes from the payload, not the generation timestamp.
- New publishers begin on probation. Their combined unverified influence is capped at 0.25 per namespace; verified outcomes raise or lower a source using a conservative confidence bound. Active integrations require at least 0.3 absolute evidence by default.
- Pointers to the latest generation are BEP 44 mutable items under a versioned identity-and-namespace key. To follow a publisher, you need only their public key. Payloads travel as torrents.
- Discovery uses one v2 DHT topic per namespace, `SHA1("hearsay:v2:" + namespace)`, plus a versioned gossip handshake that exchanges publisher keys. Feed metadata and retained feed data are bounded per namespace; failed pulls back off, repeatedly failing feeds enter quarantine, and stale generations are evicted.
- `follow` is an allowlist. If you list publishers, the daemon uses only those keys. It does not announce on the DHT topic, it ignores keys from gossip, and it discards feeds that an earlier run discovered.
