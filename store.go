package hearsay

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Retention is the number of generations kept per feed per namespace.
// Expiry happens by generations falling out of this window; deletion
// is implicit.
const Retention = 8

const truthRecLen = 48

var ErrStale = errors.New("hearsay: generation not newer than retained")

// Observation is a locally observed ground truth value.
type Observation struct {
	Value float64
	At    time.Time
}

// store holds local ground truth, retained remote generations, and
// nothing else. Ground truth is an append-only log per namespace,
// compacted on open. Generations are one file each, pruned to the
// retention window.
type store struct {
	dir   string
	mu    sync.RWMutex
	truth map[string]map[Key]Observation
	logs  map[string]*os.File
	feeds map[string]map[string][]*Generation
}

func openStore(dir string) (*store, error) {
	s := &store{
		dir:   dir,
		truth: map[string]map[Key]Observation{},
		logs:  map[string]*os.File{},
		feeds: map[string]map[string][]*Generation{},
	}
	if err := s.loadTruth(); err != nil {
		return nil, err
	}
	if err := s.loadFeeds(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *store) loadTruth() error {
	dir := filepath.Join(s.dir, "truth")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		ns, ok := strings.CutSuffix(e.Name(), ".truth")
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		// Records written before observe clamped its timestamps may sit
		// in the future, so clamp on the way in as well and rewrite the
		// log when any record moves.
		now := time.Now()
		clamped := false
		m := map[Key]Observation{}
		for i := 0; i+truthRecLen <= len(raw); i += truthRecLen {
			var k Key
			copy(k[:], raw[i:])
			at := time.Unix(0, int64(binary.BigEndian.Uint64(raw[i+40:])))
			if at.After(now) {
				at, clamped = now, true
			}
			m[k] = Observation{
				Value: math.Float64frombits(binary.BigEndian.Uint64(raw[i+32:])),
				At:    at,
			}
		}
		if len(m) > 0 {
			s.truth[ns] = m
		}
		if clamped || len(m)*truthRecLen < len(raw) {
			compact := make([]byte, 0, len(m)*truthRecLen)
			for k, o := range m {
				compact = appendTruthRec(compact, k, o)
			}
			tmp := filepath.Join(dir, e.Name()+".tmp")
			if err := os.WriteFile(tmp, compact, 0o644); err != nil {
				return err
			}
			if err := os.Rename(tmp, filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendTruthRec(b []byte, k Key, o Observation) []byte {
	b = append(b, k[:]...)
	b = binary.BigEndian.AppendUint64(b, math.Float64bits(o.Value))
	return binary.BigEndian.AppendUint64(b, uint64(o.At.UnixNano()))
}

func (s *store) loadFeeds() error {
	root := filepath.Join(s.dir, "feeds")
	feedDirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, fd := range feedDirs {
		nsDirs, err := os.ReadDir(filepath.Join(root, fd.Name()))
		if err != nil {
			continue
		}
		for _, nd := range nsDirs {
			dir := filepath.Join(root, fd.Name(), nd.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			var gens []*Generation
			for _, f := range files {
				raw, err := os.ReadFile(filepath.Join(dir, f.Name()))
				if err != nil {
					continue
				}
				// Future-dated generations are dropped here too, so a
				// store written before Ingest bounded BuiltAt heals.
				g, err := ParseGeneration(raw)
				if err != nil || g.FeedID() != fd.Name() || g.Namespace != nd.Name() ||
					g.BuiltAt.After(time.Now().Add(maxSkew)) {
					os.Remove(filepath.Join(dir, f.Name()))
					continue
				}
				gens = append(gens, g)
			}
			if len(gens) == 0 {
				continue
			}
			slices.SortFunc(gens, func(a, b *Generation) int {
				return cmp.Compare(a.Seq, b.Seq)
			})
			for len(gens) > Retention {
				os.Remove(genPath(dir, gens[0].Seq))
				gens = gens[1:]
			}
			byFeed := s.feeds[nd.Name()]
			if byFeed == nil {
				byFeed = map[string][]*Generation{}
				s.feeds[nd.Name()] = byFeed
			}
			byFeed[fd.Name()] = gens
		}
	}
	return nil
}

func genPath(dir string, seq uint64) string {
	return filepath.Join(dir, fmt.Sprintf("%d.gen", seq))
}

// observe records ground truth. Writes are ordered by at, so a caller
// timestamp is clamped to now: a future one would win against every
// later observation and pin the value permanently, including against
// the outcomes that are supposed to correct it.
func (s *store) observe(ns string, key Key, value float64, at time.Time) error {
	if now := time.Now(); at.After(now) {
		at = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.truth[ns]
	if m == nil {
		m = map[Key]Observation{}
		s.truth[ns] = m
	}
	if prev, ok := m[key]; ok && prev.At.After(at) {
		return nil
	}
	m[key] = Observation{Value: value, At: at}
	f := s.logs[ns]
	if f == nil {
		dir := filepath.Join(s.dir, "truth")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, ns+".truth"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		s.logs[ns] = f
	}
	_, err := s.logs[ns].Write(appendTruthRec(nil, key, Observation{Value: value, At: at}))
	return err
}

func (s *store) addGeneration(g *Generation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	byFeed := s.feeds[g.Namespace]
	if byFeed == nil {
		byFeed = map[string][]*Generation{}
		s.feeds[g.Namespace] = byFeed
	}
	gens := byFeed[g.FeedID()]
	if len(gens) > 0 && gens[len(gens)-1].Seq >= g.Seq {
		return ErrStale
	}
	dir := filepath.Join(s.dir, "feeds", g.FeedID(), g.Namespace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := g.MarshalBinary()
	if err != nil {
		return err
	}
	if err := os.WriteFile(genPath(dir, g.Seq), raw, 0o644); err != nil {
		return err
	}
	gens = append(gens, g)
	for len(gens) > Retention {
		os.Remove(genPath(dir, gens[0].Seq))
		gens = gens[1:]
	}
	byFeed[g.FeedID()] = gens
	return nil
}

func (s *store) forget(ns, feed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.feeds[ns][feed]; !ok {
		return nil
	}
	delete(s.feeds[ns], feed)
	return os.RemoveAll(filepath.Join(s.dir, "feeds", feed, ns))
}

func (s *store) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	for _, f := range s.logs {
		err = errors.Join(err, f.Close())
	}
	s.logs = map[string]*os.File{}
	return err
}
