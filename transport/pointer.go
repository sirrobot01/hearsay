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
	"github.com/anacrolix/torrent/bencode"
)

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
	return sha1.Sum([]byte("hearsay:v1:" + ns))
}

func pointerTarget(pub ed25519.PublicKey, ns string) bep44.Target {
	return sha1.Sum(append(append(make([]byte, 0, 32+len(ns)), pub...), ns...))
}

// PutPointer publishes the pointer under SHA1(pubkey ++ namespace) on
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
	salt := []byte(ns)
	target := pointerTarget(pub, ns)
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
	salt := []byte(ns)
	target := pointerTarget(pub, ns)
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
