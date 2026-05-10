// Package render bakes the public site to static files.
//
// The pipeline owns every derived artifact under PublicDir: post pages,
// paginated indexes, feeds, sitemaps, robots.txt, hashed theme assets,
// image variants, and unguessable draft preview pages. Each artifact is
// produced by a Stage. Adding a new derived output is a matter of
// implementing one more Stage and registering it.
//
// All writes are atomic per file (temp + fsync + rename). The HTTP
// layer reads PublicDir via http.FileServer and never observes a
// partially written file.
package render

import "context"

// Stage emits a set of files. Stages are pure: they take a Snapshot and
// return outputs. The Pipeline coordinates the actual disk I/O so
// stages have no concept of where files land or whether they've changed.
//
// A stage's Build may return outputs and a non-nil error simultaneously
// — the pipeline writes the partial output set but suppresses the
// orphan-file GC pass for that build. That keeps a stage failure from
// silently deleting files whose owners didn't get a chance to declare
// them.
type Stage interface {
	Name() string
	Build(ctx context.Context, snap *Snapshot) ([]Output, error)
}
