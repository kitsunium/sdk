// Package wire — hands the external suite each version of the encoding of
// a SnapshotMeta, whichever its cluster version selects (#546).
package wire

import coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"

// AppendSnapshotMetaV1ForTest appends m in version 1, whatever its cluster
// version: the encoding the release before writes.
func AppendSnapshotMetaV1ForTest(dst []byte, m *coreconsensus.SnapshotMetaValue) []byte {
	return appendMeta(dst, metaVersion1, m)
}

// AppendSnapshotMetaV2ForTest appends m in version 2, whatever its cluster
// version: the fields of version 1 under the version byte 2, then the
// CRC-32C of every byte before it.
func AppendSnapshotMetaV2ForTest(dst []byte, m *coreconsensus.SnapshotMetaValue) []byte {
	return appendMeta(dst, metaVersion2, m)
}

// AppendSnapshotMetaV3ForTest appends m in version 3, whatever its cluster
// version: the fields of version 1 under the version byte 3, the retired
// keys of its configuration, then the CRC-32C of every byte before it
// (#648).
func AppendSnapshotMetaV3ForTest(dst []byte, m *coreconsensus.SnapshotMetaValue) []byte {
	return appendMeta(dst, metaVersion3, m)
}
