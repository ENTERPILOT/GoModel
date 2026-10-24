package encryption

import (
	"fmt"
)

// kdfParams are Argon2id cost parameters, stored with every locally wrapped
// data key so the defaults can change without stranding existing databases.
type kdfParams struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// defaultKDFParams is the first Argon2id configuration the OWASP Password
// Storage Cheat Sheet recommends: 19 MiB of memory, 2 iterations, 1 lane. The
// derivation runs once per data key at startup, not per request.
var defaultKDFParams = kdfParams{MemoryKiB: 19 * 1024, Time: 2, Threads: 1}

// Upper bounds on stored parameters. The encryption_keys row is not trusted to
// choose the cost: a tampered row must not be able to exhaust memory at
// startup.
const (
	maxKDFMemoryKiB = 1 << 20 // 1 GiB
	maxKDFTime      = 16
	maxKDFThreads   = 16
)

func (p kdfParams) String() string {
	return fmt.Sprintf("m=%d,t=%d,p=%d", p.MemoryKiB, p.Time, p.Threads)
}

func parseKDFParams(s string) (kdfParams, error) {
	var p kdfParams
	if _, err := fmt.Sscanf(s, "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return kdfParams{}, fmt.Errorf("invalid argon2id parameters %q", s)
	}
	if p.MemoryKiB == 0 || p.MemoryKiB > maxKDFMemoryKiB ||
		p.Time == 0 || p.Time > maxKDFTime ||
		p.Threads == 0 || p.Threads > maxKDFThreads {
		return kdfParams{}, fmt.Errorf("argon2id parameters %q are out of range", s)
	}
	return p, nil
}
