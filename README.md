# Hearsay

Hearsay shares observations about external state between independent operators. The observations are byproducts of work the operators do anyway.

Hearsay is a hint layer, not an authority. An answer tells you what other operators believe, how strongly, and how recently. It does not tell you what is true.

## How it works

1. Your tools do normal work and learn facts. Example: an add to a debrid service shows if a torrent is cached.
2. Hearsay records these facts as local ground truth.
3. Hearsay publishes signed snapshots of your truth, called generations. Other operators fetch yours. You fetch theirs.
4. A query returns a value, a confidence, and an age. Local truth always wins.
5. You act on a hint. The outcome reveals the truth. Hearsay adjusts each source's weight from this feedback.

A wrong source loses weight automatically. There is no voting, no global reputation, and no moderation.

**The publication rule:** publish only what you observed as a byproduct of real work. Never probe. The `Domain` interface has no probe method by design.

## Install

On Linux or macOS, run the install script:

```
curl -fsSL https://hearsay.decypharr.com/install.sh | sh
```

The script installs the correct binary for your system, verifies its checksum, and registers a service (systemd on Linux, launchd on macOS). The daemon then runs and starts on boot.

On Windows, download the zip from the [releases page](https://github.com/sirrobot01/hearsay/releases). Releases cover Linux, macOS, and Windows, on amd64 and arm64, with a `checksums.txt`.

To build from source instead:

```
go build ./cmd/hearsay
```

## Run the daemon

```
hearsay serve
```

No configuration is necessary. The daemon creates a keypair on the first run, publishes your observations, and discovers publishers on the DHT. It works behind CGNAT. To change defaults:

```yaml
# Loaded from <user config dir>/hearsay/config.yaml if present,
# e.g. ~/.config/hearsay/config.yaml. Or: hearsay serve --config <path>
dir: /var/lib/hearsay
listen: "127.0.0.1:8477"
hearsay:
  enabled: true        # one flag to disable all network participation
  publish: true        # contribute observations
  interval: "30m"      # publish and fetch cycle
  port: 0              # swarm listen port; 0 = random
  gossip_port: 0       # discovery listen port; 0 = random
  follow: []           # empty = discover publishers on the DHT
adapters:
  debrid: [realdebrid, alldebrid, torbox, premiumize]
  usenet: [omicron, usenetexpress, abavia]
```

## HTTP API

```
GET  /v1/claim?ns=debrid.realdebrid.cached&subject=<infohash>
POST /v1/claims        {"ns": "...", "subjects": ["...", "..."]}   # batch; the primary path
POST /v1/observations  {"ns": "...", "observations": [{"subject": "...", "value": true}]}
POST /v1/outcomes      {"ns": "...", "subject": "...", "outcome": false}
GET  /v1/status
```

An answer is never a bare boolean. Set your own threshold:

```json
{"value": true, "confidence": 0.92, "sources": 4, "age_seconds": 21600, "coverage": 0.81}
```

Push observations from your own work. Report outcomes after you act on a hint: this corrects the source weights.

## Torznab proxy

```
hearsay torznab --upstream http://prowlarr:9696
```

Point your *arr at the proxy instead of the indexer. The proxy adds `hearsay_confidence` and `hearsay_cached` attributes to each release and moves claimed-cached releases to the top. On any failure, it returns the upstream result untouched.

## Embed the library

```go
h, _ := hearsay.New(dir, debrid.New(debrid.RealDebrid))
h.Observe("debrid.realdebrid.cached", infohash, 1, time.Now()) // your work learned this
a, _ := h.Query("debrid.realdebrid.cached", infohash)          // ask before you act
h.Report("debrid.realdebrid.cached", infohash, wasInstant)     // the outcome corrects weights
```

Add the `transport.Syncer` to participate in the network. See `cmd/hearsay` for the wiring.

## Add a domain

Implement the `Domain` interface. A domain must satisfy all four criteria:

1. The state is expensive or impossible to query at scale.
2. Many independent parties observe it as a byproduct of normal work.
3. A wrong answer is cheap to act on, and the action reveals the truth.
4. Subjects have stable identifiers that each party can compute alone.

## Privacy

- Hearsay publishes only what you observed as cached or complete. Never what you searched for or wanted.
- Bloom filters are testable, not enumerable. An adversary cannot list your subjects, but can test one they already hold.
- Each generation is signed with a fresh key. Your identity signs only the rotation, which limits correlation over time.
- DHT topics are public: participation puts your IP on a per-namespace list. Set `enabled: false` to opt out of everything.

## Protocol notes

- A claim is `(namespace, subject, value, observed_at)` — a claim, not a fact.
- Generations are immutable and signed. Boolean namespaces encode as bloom filters; scalar as sorted key-value tables. Consumers keep the last 8 generations per feed; old claims expire by falling out of the window.
- Pointers to the latest generation are BEP 44 mutable items at `SHA1(pubkey ++ namespace)`. To follow a publisher, you need only their public key. Payloads travel as torrents.
- Discovery: one DHT topic per namespace, `SHA1("hearsay:v1:" + namespace)`, plus a small gossip handshake that exchanges publisher keys.
