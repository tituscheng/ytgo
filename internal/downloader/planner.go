// Package downloader implements segmented HTTP downloading with resume support.
package downloader

import (
	"fmt"
)

// ByteRange represents a contiguous byte range within a file.
type ByteRange struct {
	Index     int
	StartByte int64
	EndByte   int64 // inclusive
}

// Size returns the number of bytes in the range.
func (r ByteRange) Size() int64 {
	return r.EndByte - r.StartByte + 1
}

// String returns the Range header value for this segment.
func (r ByteRange) String() string {
	return fmt.Sprintf("bytes=%d-%d", r.StartByte, r.EndByte)
}

// PlanSegments splits a file of totalSize bytes into segments.
// It respects both minChunkSize and maxChunkSize. The number of segments is
// at most maxSegments, but may be larger if needed to keep each chunk under
// maxChunkSize.
func PlanSegments(totalSize int64, maxSegments int, minChunkSize int64, maxChunkSize int64) []ByteRange {
	if totalSize <= 0 {
		return nil
	}
	if minChunkSize <= 0 {
		minChunkSize = 5 * 1024 * 1024 // 5 MB default
	}
	if maxChunkSize <= 0 {
		maxChunkSize = 10*1024*1024 - 1 // ~10 MB default
	}

	// Determine segment count from min chunk size
	segCount := int(totalSize / minChunkSize)
	if segCount < 1 {
		segCount = 1
	}
	if segCount > maxSegments && maxSegments > 1 {
		segCount = maxSegments
	}

	chunkSize := totalSize / int64(segCount)

	// Enforce maximum chunk size
	if chunkSize > maxChunkSize {
		chunkSize = maxChunkSize
		segCount = int(totalSize / chunkSize)
		if totalSize%chunkSize > 0 {
			segCount++
		}
	}

	if segCount <= 1 {
		return nil
	}

	segments := make([]ByteRange, 0, segCount)

	var start int64
	for i := 0; i < segCount; i++ {
		end := start + chunkSize - 1
		if i == segCount-1 {
			end = totalSize - 1 // last segment gets any remainder
		}
		segments = append(segments, ByteRange{
			Index:     i,
			StartByte: start,
			EndByte:   end,
		})
		start = end + 1
	}
	return segments
}
