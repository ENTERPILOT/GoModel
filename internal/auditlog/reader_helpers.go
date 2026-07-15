package auditlog

import (
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// clampLimitOffset applies the audit log reader pagination policy:
// limit defaults to 25 and is capped at 100; offset floors at 0.
func clampLimitOffset(limit, offset int) (int, int) {
	return sqlutil.ClampLimitOffset(limit, offset, 25, 100)
}
