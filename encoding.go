package hearsay

import (
	"bytes"
	"encoding/binary"
	"maps"
	"math"
	"math/bits"
	"slices"
	"sort"
	"time"
)

const (
	boolPayloadHeaderLen = 4
	boolBucketHeaderLen  = 16
	scalarRecLen         = 48
)

func encodeBoolPayload(observations map[Key]Observation, policy FreshnessPolicy, now time.Time) ([]byte, int) {
	cutoff := now.Add(-policy.MaxAge)
	buckets := map[int64][]Key{}
	for key, observation := range observations {
		if observation.Value < 0.5 || observation.At.Before(cutoff) {
			continue
		}
		at := observation.At
		if at.After(now) {
			at = now
		}
		bucket := at.UTC().Truncate(policy.Bucket).Unix()
		buckets[bucket] = append(buckets[bucket], key)
	}

	starts := slices.Sorted(maps.Keys(buckets))
	payload := make([]byte, boolPayloadHeaderLen)
	binary.BigEndian.PutUint32(payload, uint32(len(starts)))
	count := 0
	for _, start := range starts {
		keys := buckets[start]
		filter := newBloom(len(keys))
		for _, key := range keys {
			filter.add(key)
		}
		payload = binary.BigEndian.AppendUint64(payload, uint64(start))
		payload = binary.BigEndian.AppendUint32(payload, uint32(len(keys)))
		payload = binary.BigEndian.AppendUint32(payload, filter.k)
		payload = append(payload, filter.bits...)
		count += len(keys)
	}
	return payload, count
}

func encodeScalarPayload(observations map[Key]Observation, policy FreshnessPolicy, now time.Time) ([]byte, int) {
	cutoff := now.Add(-policy.MaxAge)
	keys := make([]Key, 0, len(observations))
	for key, observation := range observations {
		if !observation.At.Before(cutoff) {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b Key) int { return bytes.Compare(a[:], b[:]) })
	payload := make([]byte, 0, len(keys)*scalarRecLen)
	for _, key := range keys {
		observation := observations[key]
		at := observation.At
		if at.After(now) {
			at = now
		}
		payload = append(payload, key[:]...)
		payload = binary.BigEndian.AppendUint64(payload, math.Float64bits(observation.Value))
		payload = binary.BigEndian.AppendUint64(payload, uint64(at.Unix()))
	}
	return payload, len(keys)
}

func validBoolPayload(payload []byte, count uint32, builtAt time.Time) bool {
	if len(payload) < boolPayloadHeaderLen {
		return false
	}
	bucketCount := binary.BigEndian.Uint32(payload)
	i := boolPayloadHeaderLen
	var total uint64
	var previous int64
	for bucket := range int(bucketCount) {
		if i+boolBucketHeaderLen > len(payload) {
			return false
		}
		observedAt := int64(binary.BigEndian.Uint64(payload[i:]))
		bucketSize := binary.BigEndian.Uint32(payload[i+8:])
		declaredHashes := binary.BigEndian.Uint32(payload[i+12:])
		i += boolBucketHeaderLen
		if bucketSize == 0 || bucket > 0 && observedAt <= previous {
			return false
		}
		if time.Unix(observedAt, 0).After(builtAt.Add(maxSkew)) {
			return false
		}
		hashes, byteCount := bloomDims(int(bucketSize))
		if declaredHashes != hashes || i+byteCount > len(payload) {
			return false
		}
		ones := 0
		for _, b := range payload[i : i+byteCount] {
			ones += bits.OnesCount8(b)
		}
		if 4*ones > 3*byteCount*8 {
			return false
		}
		i += byteCount
		total += uint64(bucketSize)
		if total > math.MaxUint32 {
			return false
		}
		previous = observedAt
	}
	return i == len(payload) && uint32(total) == count
}

func validScalarPayload(payload []byte, count uint32, builtAt time.Time) bool {
	if uint64(len(payload)) != uint64(count)*scalarRecLen {
		return false
	}
	for i := range int(count) {
		offset := i * scalarRecLen
		if i > 0 && bytes.Compare(payload[offset-scalarRecLen:offset-scalarRecLen+32], payload[offset:offset+32]) >= 0 {
			return false
		}
		value := math.Float64frombits(binary.BigEndian.Uint64(payload[offset+32:]))
		observedAt := int64(binary.BigEndian.Uint64(payload[offset+40:]))
		if math.IsNaN(value) || math.IsInf(value, 0) || time.Unix(observedAt, 0).After(builtAt.Add(maxSkew)) {
			return false
		}
	}
	return true
}

func payloadLookup(valueType ValueType, payload []byte, key Key) (float64, time.Time, bool) {
	if valueType == Bool {
		if len(payload) < boolPayloadHeaderLen {
			return 0, time.Time{}, false
		}
		bucketCount := binary.BigEndian.Uint32(payload)
		i := boolPayloadHeaderLen
		var valueAt time.Time
		for range int(bucketCount) {
			if i+boolBucketHeaderLen > len(payload) {
				return 0, time.Time{}, false
			}
			observedAt := int64(binary.BigEndian.Uint64(payload[i:]))
			count := binary.BigEndian.Uint32(payload[i+8:])
			hashes := binary.BigEndian.Uint32(payload[i+12:])
			i += boolBucketHeaderLen
			_, byteCount := bloomDims(int(count))
			if count == 0 || i+byteCount > len(payload) {
				return 0, time.Time{}, false
			}
			filter := bloom{k: hashes, bits: payload[i : i+byteCount]}
			if hashes > 0 && filter.test(key) {
				valueAt = time.Unix(observedAt, 0)
			}
			i += byteCount
		}
		if valueAt.IsZero() {
			return 0, time.Time{}, false
		}
		return 1, valueAt, true
	}

	count := len(payload) / scalarRecLen
	i, ok := sort.Find(count, func(i int) int {
		return bytes.Compare(key[:], payload[i*scalarRecLen:i*scalarRecLen+32])
	})
	if !ok {
		return 0, time.Time{}, false
	}
	offset := i * scalarRecLen
	value := math.Float64frombits(binary.BigEndian.Uint64(payload[offset+32:]))
	observedAt := int64(binary.BigEndian.Uint64(payload[offset+40:]))
	return value, time.Unix(observedAt, 0), true
}
