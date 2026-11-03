package docstore

import coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"

// Compile-time assertions that each engine still answers the ports
// core/data/docstore declares for it: the file engine the context-free ports,
// the SQL engine the ports with a context on every call, and both the hooks.
//
// They matter because nothing else would notice. Open and OpenSQL return the
// ENGINES, not the ports, so a method renamed or re-typed on one engine would
// still compile everywhere in this package and break only a caller that holds
// the port — or a facade alias that promises the engine satisfies it.
var (
	_ coredocstore.Collection[struct{}]        = (*Store[struct{}])(nil)
	_ coredocstore.Versioned[struct{}]         = (*Store[struct{}])(nil)
	_ coredocstore.Trashable                   = (*Store[struct{}])(nil)
	_ coredocstore.KeyLister                   = (*Store[struct{}])(nil)
	_ coredocstore.VersionWriter               = (*Store[struct{}])(nil)
	_ coredocstore.VersionWeigher              = (*Store[struct{}])(nil)
	_ coredocstore.Revisioned[struct{}]        = (*Store[struct{}])(nil)
	_ coredocstore.Batcher[struct{}]           = (*Store[struct{}])(nil)
	_ coredocstore.Pager[struct{}]             = (*Store[struct{}])(nil)
	_ coredocstore.Ranged[struct{}]            = (*Store[struct{}])(nil)
	_ coredocstore.BatchWriter[struct{}]       = (*batchWriter[struct{}])(nil)
	_ coredocstore.Announcer                   = (*Store[struct{}])(nil)
	_ coredocstore.CollectionContext[struct{}] = (*SQLStore[struct{}])(nil)
	_ coredocstore.VersionedContext[struct{}]  = (*SQLStore[struct{}])(nil)
	_ coredocstore.TrashableContext            = (*SQLStore[struct{}])(nil)
	_ coredocstore.KeyListerContext            = (*SQLStore[struct{}])(nil)
	_ coredocstore.VersionWriterContext        = (*SQLStore[struct{}])(nil)
	_ coredocstore.VersionWeigherContext       = (*SQLStore[struct{}])(nil)
	_ coredocstore.RevisionedContext[struct{}] = (*SQLStore[struct{}])(nil)
	_ coredocstore.BatcherContext[struct{}]    = (*SQLStore[struct{}])(nil)
	_ coredocstore.PagerContext[struct{}]      = (*SQLStore[struct{}])(nil)
	_ coredocstore.RangedContext[struct{}]     = (*SQLStore[struct{}])(nil)
	_ coredocstore.BatchWriter[struct{}]       = (*sqlBatchWriter[struct{}])(nil)
	_ coredocstore.Announcer                   = (*SQLStore[struct{}])(nil)
)
