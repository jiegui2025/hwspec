package main

import (
	"crypto/sha256"
	"sync"

	"github.com/jiegui2025/hwspec/internal/ids"
)

// The bundle tests build and verify many bundles from the same real
// databases; validating each distinct content once keeps them fast. A
// tampered database has different content, so it is still validated.
func init() {
	type key struct {
		kind ids.Kind
		sum  [sha256.Size]byte
	}
	type result struct {
		n   int
		err error
	}
	var (
		mu   sync.Mutex
		seen = map[key]result{}
	)
	real := validate
	validate = func(k ids.Kind, content []byte) (int, error) {
		kk := key{k, sha256.Sum256(content)}
		mu.Lock()
		r, ok := seen[kk]
		mu.Unlock()
		if !ok {
			r.n, r.err = real(k, content)
			mu.Lock()
			seen[kk] = r
			mu.Unlock()
		}
		return r.n, r.err
	}
}
