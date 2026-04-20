package server

import "gomodel/internal/gateway"

// TranslatedRequestPatcher applies request-level transforms for translated
// routes after workflow resolution has resolved the concrete execution selector.
type TranslatedRequestPatcher = gateway.TranslatedRequestPatcher
