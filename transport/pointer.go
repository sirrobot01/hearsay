package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anacrolix/dht/v2/bep44"
	"github.com/anacrolix/dht/v2/exts/getput"
	"github.com/anacrolix/dht/v2/krpc"
	alog "github.com/anacrolix/log"
	"github.com/anacrolix/torrent/bencode"
)

// quietDHT silences the per-node chatter of a DHT traversal. A put
// fans out to the closest nodes and logs every one that does not
// answer at warning level; a get logs every node that answers wrong.
// Both are routine — the operation succeeds on the nodes that do
// answer — but the target of a pointer is versioned and never changes,
// so the same unresponsive neighbour is re-selected
// and re-logged on every republish, forever. The caller reports the
// outcome that matters: whether the whole operation failed.
func quietDHT(ctx context.Context) context.Context {
	return alog.ContextWithLogger(ctx, alog.ContextLogger(ctx).WithFilterLevel(alog.Error))
}

// Pointer is the BEP 44 mutable item a publisher maintains per
// namespace: where to fetch the latest generation. Signing and
// monotonic seq replay protection are properties of BEP 44 itself.
// Subscribing to a publisher requires only their public key.
type Pointer struct {
	Gen      uint64 `json:"gen"`
	InfoHash string `json:"infohash"`
	Size     int64  `json:"size"`
	BuiltAt  int64  `json:"built_at"`
}

// Topic is the synthetic DHT rendezvous for a namespace.
func Topic(ns string) [20]byte {
	return sha1.Sum([]byte("hearsay:v2:" + ns))
}

func pointerTarget(pub ed25519.PublicKey, ns string) bep44.Target {
	salt := pointerSalt(ns)
	return sha1.Sum(append(append(make([]byte, 0, 32+len(salt)), pub...), salt...))
}

func pointerSalt(ns string) []byte {
	return []byte("hsy2:" + ns)
}

// PutPointer publishes the pointer under the versioned identity and namespace on
// every DHT server the node runs. Items expire in roughly two hours;
// callers republish continuously.
func (n *Node) PutPointer(ctx context.Context, id ed25519.PrivateKey, ns string, p Pointer) error {
	v, err := json.Marshal(p)
	if err != nil {
		return err
	}
	pub := id.Public().(ed25519.PublicKey)
	var k [32]byte
	copy(k[:], pub)
	salt := pointerSalt(ns)
	target := pointerTarget(pub, ns)
	ctx = quietDHT(ctx)
	errs := errors.New("transport: no dht servers")
	for _, srv := range n.dhts {
		_, err := getput.Put(ctx, krpc.ID(target), srv, salt, func(seq int64) bep44.Put {
			put := bep44.Put{V: string(v), K: &k, Salt: salt, Seq: max(seq+1, int64(p.Gen))}
			put.Sign(id)
			return put
		})
		if err == nil {
			return nil
		}
		errs = errors.Join(errs, err)
	}
	return errs
}

// GetPointer fetches and verifies a publisher's pointer for a
// namespace. The DHT is a refresh channel, not storage: callers cache
// pointers locally.
func (n *Node) GetPointer(ctx context.Context, pub ed25519.PublicKey, ns string) (Pointer, error) {
	salt := pointerSalt(ns)
	target := pointerTarget(pub, ns)
	ctx = quietDHT(ctx)
	errs := errors.New("transport: no dht servers")
	for _, srv := range n.dhts {
		res, _, err := getput.Get(ctx, target, srv, nil, salt)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		if !res.Mutable {
			continue
		}
		var s string
		if err := bencode.Unmarshal(res.V, &s); err != nil {
			continue
		}
		var p Pointer
		if err := json.Unmarshal([]byte(s), &p); err != nil {
			continue
		}
		if len(p.InfoHash) != 40 || p.Size <= 0 {
			continue
		}
		return p, nil
	}
	return Pointer{}, fmt.Errorf("transport: pointer for %s: %w", ns, errs)
}
