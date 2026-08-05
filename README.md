# Hearsay

Hearsay is a protocol and Go library. It shares observations about external state between independent operators. The observations are byproducts of work the operators do anyway.

Hearsay is a hint layer. It is never an authority. An answer tells you what other operators believe, how strongly, and how recently. It does not tell you what is true.

## How it works

1. Your tools do their normal work. This work reveals facts. For example, an add to a debrid service shows if a torrent is cached.
2. Hearsay records these facts as local ground truth.
3. Hearsay publishes signed snapshots of your ground truth. These snapshots are called generations.
4. Other operators fetch your generations. You fetch theirs.
5. You query all retained generations locally. A query returns a value, a confidence, and an age.
6. You act on a hint. The outcome shows the truth. Hearsay adjusts the weight of each source from this feedback.

A wrong source loses weight automatically. There is no voting, no global reputation, and no moderation.

## The publication rule

Publish only what you observed as a byproduct of real work. Never probe to generate data.

This rule is part of the protocol. The `Domain` interface has no probe method. There is no supported path for an adapter to start a query against the observed system.

## Components

| Component | Description |
|---|---|
| `hearsay` (library) | The core library. Embed it in a Go application. |
| `hearsay serve` | The daemon with an HTTP query API. |
| `hearsay torznab` | A proxy between an *arr and its Torznab indexer. It reorders releases by cache confidence. |
| `debrid` | Adapter: debrid cache availability. Boolean. |
| `usenet` | Adapter: usenet completeness by backbone. Scalar. |

## Install

Build the CLI:

```
go build ./cmd/hearsay
```

## Start the daemon

Start the daemon with no configuration:

```
./hearsay serve
```

The daemon creates a keypair on the first run. It stores data in your user configuration directory. It publishes your observations and follows discovered publishers. You do not need an account, a server, a port forward, or a key ceremony. The daemon operates correctly behind CGNAT.

To change the defaults, write a configuration file:

```yaml
dir: /var/lib/hearsay
listen: "127.0.0.1:8477"
hearsay:
  enabled: true        # one flag to disable all network participation
  publish: true        # contribute observations
  interval: "30m"      # publish and fetch cycle
  port: 0              # swarm listen port; 0 = random
  follow:              # empty = discover publishers on the DHT
    - pubkey: "ed25519:a3f9..."
      label: "large-library-hourly-repair"
adapters:
  debrid: [realdebrid, alldebrid, torbox, premiumize]
  usenet: [omicron, usenetexpress, abavia]
```

Start the daemon with the file:

```
./hearsay serve --config config.yaml
```

## Query the daemon

Get one hint:

```
GET /v1/claim?ns=debrid.realdebrid.cached&subject=<infohash>
```

The response is never a bare boolean. Set your own threshold:

```json
{"value": true, "confidence": 0.92, "sources": 4, "age_seconds": 21600, "coverage": 0.81}
```

Get many hints in one call. This is the primary path:

```
POST /v1/claims
{"ns": "debrid.realdebrid.cached", "subjects": ["<infohash>", "..."]}
```

Push an observation from your own work:

```
POST /v1/observations
{"ns": "debrid.realdebrid.cached", "observations": [{"subject": "<infohash>", "value": true}]}
```

Report the outcome after you act on a hint. This step corrects the source weights:

```
POST /v1/outcomes
{"ns": "debrid.realdebrid.cached", "subject": "<infohash>", "outcome": false}
```

## Use the Torznab proxy

Point your *arr at the proxy instead of the indexer:

```
./hearsay torznab --upstream http://prowlarr:9696 --hearsayd http://127.0.0.1:8477
```

The proxy forwards each request unchanged. On search results, it adds `hearsay_confidence` and `hearsay_cached` attributes to each release. It moves claimed-cached releases to the top. If anything fails, it returns the upstream result untouched.

## Embed the library

```go
h, err := hearsay.New(dir, debrid.New(debrid.RealDebrid))

// Your repair cycle already knows this torrent is cached.
h.Observe("debrid.realdebrid.cached", infohash, 1, time.Now())

// Ask before you add.
a, err := h.Query("debrid.realdebrid.cached", infohash)
if a.Confidence > 0.8 { /* your threshold, your call */ }

// You acted. The outcome reveals the truth.
h.Report("debrid.realdebrid.cached", infohash, wasInstant)
```

Add the `transport.Syncer` to participate in the network. See `cmd/hearsay` for the full wiring.

## Add a domain

A domain must satisfy four criteria. All four are mandatory:

1. The state is expensive or impossible to query at scale.
2. Many independent parties observe it as a byproduct of normal work.
3. A wrong answer is cheap to act on, and the action reveals the truth.
4. Subjects have stable identifiers that each party can compute alone.

Implement the `Domain` interface: `Namespace`, `Encode`, `ValueType`, `Decay`, and `Verify`. The interface has no probe method by design.

## Privacy

Read this section before you enable publishing.

- Hearsay publishes only what you observed as cached or complete. It never publishes what you searched for, wanted, or attempted.
- Bloom filters are testable, not enumerable. An adversary cannot list your subjects. An adversary who already holds a subject list can test membership against your feed.
- Generations are signed with a fresh key each time. Your long-term identity signs only the key rotation. This limits correlation across time.
- DHT topics are public. Participation in a namespace topic puts your IP on a categorized list. You are already in torrent swarms with a public IP, but a categorized list is more specific. Set `enabled: false` to opt out of all of it.

## Protocol summary

- A claim is `(namespace, subject, value, observed_at)`. The word is deliberate: a claim, not a fact.
- A generation is an immutable, signed snapshot of one publisher's claims in one namespace. Boolean namespaces encode as a bloom filter. Scalar namespaces encode as a sorted key-value table.
- A feed is one publisher's sequence of generations, identified by an ed25519 public key.
- Consumers keep the last 8 generations per feed. Old claims expire when generations leave this window.
- Pointers to the latest generation live in the DHT as BEP 44 mutable items at `SHA1(pubkey ++ namespace)`. To follow a publisher, you need only their public key.
- Generation payloads travel as torrents. Popular publishers do not carry the distribution load.
- Discovery uses one DHT topic per namespace: `SHA1("hearsay:v1:" + namespace)`. Participants exchange known publisher keys over a small gossip handshake.
- Local ground truth always wins over remote claims. It is not a hint.
