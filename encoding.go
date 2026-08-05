package hearsay

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
	"sort"
)

// Bool payloads are a bloom filter: 4-byte hash count followed by the
// bit array. Scalar and Enum payloads are sorted fixed-width records
// of key and float64 value, queried by binary search; values
// round-trip exactly.

const scalarRecLen = 40

func encodeBoolPayload(keys []Key) []byte {
	b := newBloom(len(keys))
	for _, k := range keys {
		b.add(k)
	}
	p := make([]byte, 4, 4+len(b.bits))
	binary.BigEndian.PutUint32(p, b.k)
	return append(p, b.bits...)
}

func encodeScalarPayload(m map[Key]float64) []byte {
	keys := make([]Key, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b Key) int { return bytes.Compare(a[:], b[:]) })
	p := make([]byte, 0, len(keys)*scalarRecLen)
	for _, k := range keys {
		p = append(p, k[:]...)
		p = binary.BigEndian.AppendUint64(p, math.Float64bits(m[k]))
	}
	return p
}

// payloadLookup tests a payload for a key. For Bool payloads a hit
// returns 1; absence is unknown, never false.
func payloadLookup(vt ValueType, p []byte, key Key) (float64, bool) {
	if vt == Bool {
		if len(p) < 5 {
			return 0, false
		}
		b := bloom{k: binary.BigEndian.Uint32(p), bits: p[4:]}
		if b.k > 0 && b.test(key) {
			return 1, true
		}
		return 0, false
	}
	n := len(p) / scalarRecLen
	i, ok := sort.Find(n, func(i int) int {
		return bytes.Compare(key[:], p[i*scalarRecLen:i*scalarRecLen+32])
	})
	if !ok {
		return 0, false
	}
	return math.Float64frombits(binary.BigEndian.Uint64(p[i*scalarRecLen+32:])), true
}
