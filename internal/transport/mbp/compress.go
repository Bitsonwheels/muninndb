package mbp

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const (
	CompressionThreshold = 1024 // Compress payloads > 1KB
)

// zstd encoder/decoder pools — lazily initialised on first use.
// Errors during pool creation are surfaced to the caller via error returns
// rather than panicking at process start, allowing graceful shutdown.
var (
	encoderPool sync.Pool
	decoderPool sync.Pool
	initOnce    sync.Once
	initErr     error
)

// initPools validates that zstd is operational on first use.
// Returns a non-nil error if the zstd library cannot create codecs.
func initPools() error {
	initOnce.Do(func() {
		encoderPool.New = func() any {
			enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
			if err != nil {
				// Return the error wrapped in a sentinel so callers can detect it.
				return fmt.Errorf("zstd encoder init: %w", err)
			}
			return enc
		}
		decoderPool.New = func() any {
			// Bound the decoder to the advertised payload limit (HelloResponse
			// Limits.MaxPayloadMB, internal/engine/engine.go:1330-1335). The wire
			// frame is ≤ MaxPayloadSize compressed; its decompressed form may be
			// up to the advertised limit. zstd enforces this bound DURING
			// decoding — a frame whose declared or measured output is larger
			// fails instead of allocating it.
			dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(64<<20))
			if err != nil {
				return fmt.Errorf("zstd decoder init: %w", err)
			}
			return dec
		}
		// Eagerly validate both codecs so failures surface at server start.
		enc := encoderPool.Get()
		if err, ok := enc.(error); ok {
			initErr = err
			return
		}
		encoderPool.Put(enc)
		dec := decoderPool.Get()
		if err, ok := dec.(error); ok {
			initErr = err
			return
		}
		decoderPool.Put(dec)
	})
	return initErr
}

// CompressPayload compresses data with zstd if it's worth it.
// Returns compressed data and true if compression was applied.
// Returns the original data unmodified if zstd is unavailable or compression
// doesn't reduce size.
func CompressPayload(data []byte) ([]byte, bool, error) {
	if len(data) <= CompressionThreshold {
		return data, false, nil
	}
	if err := initPools(); err != nil {
		return data, false, nil // degrade gracefully: send uncompressed
	}

	raw := encoderPool.Get()
	if err, ok := raw.(error); ok {
		return data, false, err
	}
	enc := raw.(*zstd.Encoder)
	defer encoderPool.Put(enc)

	compressed := enc.EncodeAll(data, nil)
	// Only use compression if it actually saves space.
	if len(compressed) < len(data) {
		return compressed, true, nil
	}
	return data, false, nil
}

// DecompressPayload decompresses zstd-compressed data. The decoder is created
// with WithDecoderMaxMemory(64<<20) — the advertised HelloResponse
// Limits.MaxPayloadMB (internal/engine/engine.go:1330-1335) — so a
// decompression bomb fails while decoding instead of after the full buffer has
// been allocated.
func DecompressPayload(data []byte) ([]byte, error) {
	if err := initPools(); err != nil {
		return nil, fmt.Errorf("zstd unavailable: %w", err)
	}

	raw := decoderPool.Get()
	if err, ok := raw.(error); ok {
		return nil, err
	}
	dec := raw.(*zstd.Decoder)
	defer decoderPool.Put(dec)

	decoded, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("zstd decompress: %w", err)
	}
	return decoded, nil
}
