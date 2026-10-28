package cli

import "io"

// e2eBuild is true only in a binary built with -tags truehand_e2e (`make e2e`), for the
// browser tests that drive the app from inside agent sessions.
var e2eBuild bool

// e2eStart checks such a binary's environment before any command; nil in real builds.
var e2eStart func(stderr io.Writer) error
