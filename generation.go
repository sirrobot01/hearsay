package hearsay

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"time"
)

// Generation is an immutable, signed snapshot of one publisher's
// claims within one namespace at one point in time.
//
// Each generation carries a fresh signing key, and the long-term
// identity key signs that key in a rotation proof. The identity is in
// every generation, and it is the feed ID that consumers follow, so
// every generation from one publisher stays linkable to the rest. The
// fresh key bounds what a leaked signing key can forge. It does not
// limit correlation.
type Generation struct {
	Namespace string
	Type      ValueType
	Seq       uint64
	BuiltAt   time.Time
	Count     uint32
	Payload   []byte
	Identity  [32]byte
	GenKey    [32]byte
	KeyProof  [64]byte
	Sig       [64]byte
}

// ProtocolVersion identifies the generation format advertised by the API.
const ProtocolVersion = "HSY2"

const genMagic = ProtocolVersion

// maxSkew bounds how far ahead of local time a generation's BuiltAt may
// be. BuiltAt is the publisher's own claim, and Decay grows without
// bound for a negative age, so an unchecked future date would hold a
// source at the freshness clamp forever while honest ones decay.
const maxSkew = 5 * time.Minute

var (
	ErrInvalidGeneration = errors.New("hearsay: invalid generation")
	ErrFutureGeneration  = errors.New("hearsay: generation built too far in the future")
)

// FeedID identifies the feed a generation belongs to: the hex-encoded
// long-term identity public key.
func (g *Generation) FeedID() string {
	return hex.EncodeToString(g.Identity[:])
}

func (g *Generation) body() []byte {
	b := make([]byte, 0, 220+len(g.Namespace)+len(g.Payload))
	b = append(b, genMagic...)
	b = binary.AppendUvarint(b, uint64(len(g.Namespace)))
	b = append(b, g.Namespace...)
	b = append(b, byte(g.Type))
	b = binary.BigEndian.AppendUint64(b, g.Seq)
	b = binary.BigEndian.AppendUint64(b, uint64(g.BuiltAt.Unix()))
	b = binary.BigEndian.AppendUint32(b, g.Count)
	b = binary.AppendUvarint(b, uint64(len(g.Payload)))
	b = append(b, g.Payload...)
	b = append(b, g.Identity[:]...)
	b = append(b, g.GenKey[:]...)
	b = append(b, g.KeyProof[:]...)
	return b
}

func (g *Generation) rotationMsg() []byte {
	b := make([]byte, 0, 26+len(g.Namespace)+40)
	b = append(b, "hearsay:v2:rotate:"...)
	b = append(b, g.Namespace...)
	b = binary.BigEndian.AppendUint64(b, g.Seq)
	b = append(b, g.GenKey[:]...)
	return b
}

// BuildGeneration signs a payload into a generation using a fresh
// per-generation key, bound to the identity key by a rotation proof.
func BuildGeneration(identity ed25519.PrivateKey, ns string, vt ValueType, seq uint64, builtAt time.Time, count int, payload []byte) (*Generation, error) {
	builtAt = builtAt.UTC().Truncate(time.Second)
	if len(identity) != ed25519.PrivateKeySize || !validNamespace(ns) || count < 0 || uint64(count) > math.MaxUint32 {
		return nil, ErrInvalidGeneration
	}
	valid := false
	switch vt {
	case Bool:
		valid = validBoolPayload(payload, uint32(count), builtAt)
	case Scalar, Enum:
		valid = validScalarPayload(payload, uint32(count), builtAt)
	}
	if !valid {
		return nil, ErrInvalidGeneration
	}
	genPub, genPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	g := &Generation{
		Namespace: ns,
		Type:      vt,
		Seq:       seq,
		BuiltAt:   builtAt,
		Count:     uint32(count),
		Payload:   payload,
	}
	copy(g.Identity[:], identity.Public().(ed25519.PublicKey))
	copy(g.GenKey[:], genPub)
	copy(g.KeyProof[:], ed25519.Sign(identity, g.rotationMsg()))
	copy(g.Sig[:], ed25519.Sign(genPriv, g.body()))
	return g, nil
}

func (g *Generation) MarshalBinary() ([]byte, error) {
	return append(g.body(), g.Sig[:]...), nil
}

// ParseGeneration decodes a generation blob and verifies both the
// rotation proof and the generation signature.
func ParseGeneration(raw []byte) (*Generation, error) {
	if len(raw) < 4 || string(raw[:4]) != genMagic {
		return nil, ErrInvalidGeneration
	}
	i := 4
	nsLen, n := binary.Uvarint(raw[i:])
	if n <= 0 || nsLen == 0 || nsLen > 255 {
		return nil, ErrInvalidGeneration
	}
	i += n
	if i+int(nsLen) > len(raw) {
		return nil, ErrInvalidGeneration
	}
	g := &Generation{Namespace: string(raw[i : i+int(nsLen)])}
	if !validNamespace(g.Namespace) {
		return nil, ErrInvalidGeneration
	}
	i += int(nsLen)
	if i+21 > len(raw) {
		return nil, ErrInvalidGeneration
	}
	g.Type = ValueType(raw[i])
	i++
	g.Seq = binary.BigEndian.Uint64(raw[i:])
	i += 8
	g.BuiltAt = time.Unix(int64(binary.BigEndian.Uint64(raw[i:])), 0).UTC()
	i += 8
	g.Count = binary.BigEndian.Uint32(raw[i:])
	i += 4
	pLen, n := binary.Uvarint(raw[i:])
	if n <= 0 || pLen > uint64(len(raw)) {
		return nil, ErrInvalidGeneration
	}
	i += n
	if i+int(pLen)+192 != len(raw) {
		return nil, ErrInvalidGeneration
	}
	g.Payload = bytes.Clone(raw[i : i+int(pLen)])
	i += int(pLen)
	i += copy(g.Identity[:], raw[i:])
	i += copy(g.GenKey[:], raw[i:])
	i += copy(g.KeyProof[:], raw[i:])
	copy(g.Sig[:], raw[i:])
	switch g.Type {
	case Bool:
		if !validBoolPayload(g.Payload, g.Count, g.BuiltAt) {
			return nil, ErrInvalidGeneration
		}
	case Scalar, Enum:
		if !validScalarPayload(g.Payload, g.Count, g.BuiltAt) {
			return nil, ErrInvalidGeneration
		}
	default:
		return nil, ErrInvalidGeneration
	}
	if !ed25519.Verify(g.Identity[:], g.rotationMsg(), g.KeyProof[:]) {
		return nil, ErrInvalidGeneration
	}
	if !ed25519.Verify(g.GenKey[:], g.body(), g.Sig[:]) {
		return nil, ErrInvalidGeneration
	}
	return g, nil
}
