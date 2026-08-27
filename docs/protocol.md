# HSY2 protocol

HSY2 transports signed claims, not facts. A claim is `(namespace, subject key, value, observed_at)`. Implementations must apply the domain's freshness and decay policy locally and must not turn a Bloom miss into a negative claim.

## Subject keys and namespaces

A domain defines a stable namespace, value type, subject encoding, freshness policy, decay function, and outcome verifier. The subject key is always 32 bytes and must be derived identically by every implementation of that domain. Namespace identifiers use lowercase ASCII letters, digits, `.`, `-`, and `_`, with a maximum length of 59 bytes.

The configured HTTP capabilities are available from `GET /v1/namespaces`.

## Generation envelope

All integers are big-endian except the two unsigned varints. A generation is:

| Field | Encoding |
|---|---|
| magic | four bytes, `HSY2` |
| namespace length | unsigned varint |
| namespace | namespace bytes |
| value type | one byte: `0` Boolean, `1` scalar, `2` enum |
| sequence | uint64 |
| built at | uint64 Unix seconds |
| claim count | uint32 |
| payload length | unsigned varint |
| payload | domain-independent encoding below |
| identity key | 32-byte Ed25519 public key |
| generation key | 32-byte Ed25519 public key |
| rotation proof | 64-byte Ed25519 signature |
| generation signature | 64-byte Ed25519 signature |

The long-term identity signs `"hearsay:v2:rotate:" || namespace || sequence || generation_key`. The generation key signs every preceding envelope byte through the rotation proof. Sequence numbers increase independently per namespace and identity.

## Payloads

Boolean payloads begin with a uint32 bucket count. Each bucket contains its uint64 Unix start, uint32 claim count, uint32 Bloom hash count, and Bloom bytes. For `n >= 1` and `p = 0.0001`, compute `m = ceil(-n*ln(p)/(ln(2)^2))`, round `m` up to a multiple of eight bits, and compute `k = max(round((m/n)*ln(2)), 1)`. Keys use `(h1 + i*h2) mod m` for `i` in `[0,k)`, where `h1` and `h2` are the first two big-endian uint64 words of the subject key. Buckets are ascending and contain only positive membership claims.

Scalar and enum payloads contain fixed 48-byte records sorted by subject key: 32 key bytes, IEEE-754 float64 bits, and uint64 observation Unix seconds.

The reference query path resolves scalar claims with a reputation-and-decay-weighted median. Only claims within `0.15` of that value contribute `support`, `evidence_weight`, and `sources`; evenly split distant values therefore remain known but carry no actionable support.

Claim age comes from the payload observation time. Rebuilding or relaying a generation never refreshes it.

## Pointer and payload transport

The current generation pointer is a signed BEP 44 mutable item. Its salt is `"hsy2:" || namespace`; its target is `SHA1(identity_public_key || salt)`. The JSON value contains generation sequence, torrent infohash, byte size, and build time.

Generation blobs travel as single-file torrents. Discovery rendezvous uses `SHA1("hearsay:v2:" || namespace)`. Gossip version 2 exchanges bounded publisher-key lists only for namespaces both nodes track.

Every envelope, pointer, namespace, payload dimension, timestamp, and size must be validated before retention. Consumers keep local trust weights; reputation is never imported from the network.
