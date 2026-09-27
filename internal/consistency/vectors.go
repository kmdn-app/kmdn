package consistency

import (
	"encoding/binary"
	"math"
	"sort"
)

// encode stores a vector as little-endian float32s.
func encode(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decode(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// normalize scales v to unit length, so cosine similarity is a dot product.
func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(n))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func dot(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// Hit is a neighbour: its index in the searched set and its similarity.
type Hit struct {
	I   int
	Sim float32
}

// Searcher finds the nearest vectors to a query. The flat implementation is
// brute force in Go (thousands of passages per repo fit comfortably and
// SQLite stays pure Go); pgvector can implement it on Postgres later.
type Searcher interface {
	Near(q []float32, k int, min float32, skip func(i int) bool) []Hit
}

// Flat is brute-force cosine search over unit vectors.
type Flat [][]float32

// Near implements Searcher: the top k above min, most similar first.
func (f Flat) Near(q []float32, k int, min float32, skip func(i int) bool) []Hit {
	var hits []Hit
	for i, v := range f {
		if skip != nil && skip(i) {
			continue
		}
		if s := dot(q, v); s >= min {
			hits = append(hits, Hit{i, s})
		}
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a].Sim > hits[b].Sim })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}
