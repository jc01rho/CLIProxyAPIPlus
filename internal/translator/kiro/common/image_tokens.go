package common

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

const kiroImageTokenFallback = 1600

// EstimateKiroImageTokens estimates ceil(width*height/750), capped at 1600.
// Decode only a bounded header; malformed or unknown images use the ceiling.
// Ported from kiro-lb src/payload_guard.rs (1581af9).
func EstimateKiroImageTokens(base64Data string) int {
	const maxBase64Header = 87384
	if len(base64Data) > maxBase64Header {
		base64Data = base64Data[:maxBase64Header]
	}
	base64Data = base64Data[:len(base64Data)/4*4]
	header, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return kiroImageTokenFallback
	}
	width, height := kiroImageDimensions(header)
	if width == 0 || height == 0 {
		return kiroImageTokenFallback
	}
	// Avoid overflow for untrusted dimensions. Beyond this area the estimate
	// is already at the ceiling, so no full multiplication is necessary.
	const maxArea = uint64(kiroImageTokenFallback * 750)
	if width > maxArea/height {
		return kiroImageTokenFallback
	}
	return int((width*height + 749) / 750)
}

func kiroImageDimensions(header []byte) (uint64, uint64) {
	// Kiro also accepts WebP; read its small dimension headers without decoding
	// pixels or introducing another image library dependency.
	if len(header) >= 16 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		switch string(header[12:16]) {
		case "VP8 ":
			if len(header) >= 30 {
				return uint64(binary.LittleEndian.Uint16(header[26:28]) & 0x3fff), uint64(binary.LittleEndian.Uint16(header[28:30]) & 0x3fff)
			}
		case "VP8L":
			if len(header) >= 25 {
				bits := binary.LittleEndian.Uint32(header[21:25])
				return uint64(bits&0x3fff) + 1, uint64((bits>>14)&0x3fff) + 1
			}
		case "VP8X":
			if len(header) >= 30 {
				width := uint64(header[24]) | uint64(header[25])<<8 | uint64(header[26])<<16
				height := uint64(header[27]) | uint64(header[28])<<8 | uint64(header[29])<<16
				return width + 1, height + 1
			}
		}
		return 0, 0
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(header))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0
	}
	return uint64(config.Width), uint64(config.Height)
}
