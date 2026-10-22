// Package wire — hands the external suite the version 2 encoding of a
// SnapshotMeta, which this release reads and never writes (#546).
package wire

import (
	"encoding/binary"
	"hash/crc32"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// AppendSnapshotMetaV2ForTest appends m as the release that writes version
// 2 will: the fields of version 1 under the version byte 2, then the
// CRC-32C of every byte before it.
func AppendSnapshotMetaV2ForTest(dst []byte, m *coreconsensus.SnapshotMetaValue) []byte {
	start := len(dst)
	dst = AppendSnapshotMeta(dst, m)
	dst[start] = metaVersion2
	return binary.LittleEndian.AppendUint32(dst, crc32.Checksum(dst[start:], castagnoli))
}
