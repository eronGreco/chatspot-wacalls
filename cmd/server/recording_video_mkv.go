package main

// A small streaming Matroska writer keeps original H.264 access units and
// millisecond presentation times without decoding on the live media path.
// https://www.matroska.org/technical/codec_specs.html#v_mpeg4isoavc
// Each segment starts with an IDR and a fixed SPS/PPS/orientation. Unknown-size
// Segment + complete Clusters are readable after an interrupted capture.
import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/purpshell/meowcaller/rtp"
)

func ebmlHeader(id uint32, size uint64) []byte {
	n := 1
	for id>>(8*n) != 0 && n < 4 {
		n++
	}
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		out[i] = byte(id)
		id >>= 8
	}
	n = 1
	for size >= (uint64(1)<<(7*n))-1 {
		n++
	}
	v := size | (uint64(1) << (7 * n))
	b := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	out = append(out, b...)
	return out
}
func ebml(id uint32, data []byte) []byte { return append(ebmlHeader(id, uint64(len(data))), data...) }
func ebmlUint(id uint32, value uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], value)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return ebml(id, b[i:])
}
func ebmlJoin(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func newVideoMKV(path string, sps, pps []byte) (*os.File, error) {
	if len(sps) < 4 || len(sps) > 65535 || len(pps) == 0 || len(pps) > 65535 {
		return nil, fmt.Errorf("invalid AVC parameter sets")
	}
	width, height, err := h264Dimensions(sps)
	if err != nil {
		return nil, err
	}
	avcc := []byte{1, sps[1], sps[2], sps[3], 255, 225, byte(len(sps) >> 8), byte(len(sps))}
	avcc = append(avcc, sps...)
	avcc = append(avcc, 1, byte(len(pps)>>8), byte(len(pps)))
	avcc = append(avcc, pps...)
	header := ebml(0x1a45dfa3, ebmlJoin(ebmlUint(0x4286, 1), ebmlUint(0x42f7, 1), ebmlUint(0x42f2, 4), ebmlUint(0x42f3, 8), ebml(0x4282, []byte("matroska")), ebmlUint(0x4287, 4), ebmlUint(0x4285, 2)))
	header = append(header, []byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}...)
	header = append(header, ebml(0x1549a966, ebmlJoin(ebmlUint(0x2ad7b1, 1000000), ebml(0x4d80, []byte("WaCalls")), ebml(0x5741, []byte("WaCalls"))))...)
	header = append(header, ebml(0x1654ae6b, ebml(0xae, ebmlJoin(ebmlUint(0xd7, 1), ebmlUint(0x73c5, 1), ebmlUint(0x83, 1), ebmlUint(0x9c, 0), ebml(0x86, []byte("V_MPEG4/ISO/AVC")), ebml(0x63a2, avcc), ebml(0xe0, ebmlJoin(ebmlUint(0xb0, uint64(width)), ebmlUint(0xba, uint64(height)))))))...)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(header); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func writeVideoMKV(w io.Writer, au []byte, ptsMS int64, key bool) error {
	if ptsMS < 0 {
		return fmt.Errorf("negative video timestamp")
	}
	nals := rtp.SplitAnnexB(au)
	length := uint64(4)
	for _, nal := range nals {
		length += uint64(4 + len(nal))
	}
	timecode := ebmlUint(0xe7, uint64(ptsMS))
	blockHeader := ebmlHeader(0xa3, length)
	prefix := []byte{0x81, 0, 0, 0}
	if key {
		prefix[3] = 0x80
	}
	for _, b := range [][]byte{ebmlHeader(0x1f43b675, uint64(len(timecode)+len(blockHeader))+length), timecode, blockHeader, prefix} {
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	// NAL payloads go straight to disk; no second full-frame buffer is needed.
	for _, nal := range nals {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(nal)))
		if _, err := w.Write(n[:]); err != nil {
			return err
		}
		if _, err := w.Write(nal); err != nil {
			return err
		}
	}
	return nil
}

// H.264 SPS RBSP parsing, including high-profile scaling lists and cropping.
// Ref: ITU-T H.264, 7.3.2.1.1. Dimensions guard malformed/unbounded inputs.
type spsBits struct {
	b   []byte
	pos int
	err bool
}

func (r *spsBits) bits(n int) uint32 {
	var v uint32
	for range n {
		if r.pos >= len(r.b)*8 {
			r.err = true
			return 0
		}
		v = v<<1 | uint32((r.b[r.pos/8]>>uint(7-r.pos%8))&1)
		r.pos++
	}
	return v
}
func (r *spsBits) ue() uint32 {
	n := 0
	for r.bits(1) == 0 && !r.err {
		n++
		if n > 30 {
			r.err = true
			return 0
		}
	}
	return (1 << n) - 1 + r.bits(n)
}
func (r *spsBits) se() int {
	v := int(r.ue())
	if v%2 == 0 {
		return -v / 2
	}
	return (v + 1) / 2
}
func h264Dimensions(sps []byte) (int, int, error) {
	if len(sps) < 4 {
		return 0, 0, fmt.Errorf("short SPS")
	}
	rbsp := make([]byte, 0, len(sps))
	zeros := 0
	for _, b := range sps[1:] {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		rbsp = append(rbsp, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	r := &spsBits{b: rbsp}
	profile := r.bits(8)
	switch profile {
	case 66, 77, 88, 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
	default:
		return 0, 0, fmt.Errorf("unsupported SPS profile")
	}
	r.bits(16)
	r.ue()
	chroma := uint32(1)
	separate := uint32(0)
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma = r.ue()
		if chroma > 3 {
			r.err = true
		}
		if chroma == 3 {
			separate = r.bits(1)
		}
		r.ue()
		r.ue()
		r.bits(1)
		if r.bits(1) != 0 {
			count := 8
			if chroma == 3 {
				count = 12
			}
			for i := 0; i < count; i++ {
				if r.bits(1) != 0 {
					size := 16
					if i >= 6 {
						size = 64
					}
					last, next := 8, 8
					for j := 0; j < size; j++ {
						if next != 0 {
							next = (last + r.se() + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	r.ue()
	poc := r.ue()
	if poc == 0 {
		r.ue()
	} else if poc == 1 {
		r.bits(1)
		r.se()
		r.se()
		count := r.ue()
		if count > 255 {
			r.err = true
			count = 0
		}
		for range count {
			r.se()
		}
	} else if poc > 2 {
		r.err = true
	}
	r.ue()
	r.bits(1)
	mw, mh := r.ue(), r.ue()
	if mw > 255 || mh > 255 {
		r.err = true
	}
	frame := r.bits(1)
	if frame == 0 {
		r.bits(1)
	}
	r.bits(1)
	var left, right, top, bottom uint32
	if r.bits(1) != 0 {
		left = r.ue()
		right = r.ue()
		top = r.ue()
		bottom = r.ue()
	}
	cx, cy := uint32(1), 2-frame
	if separate == 0 && chroma != 0 {
		if chroma == 1 || chroma == 2 {
			cx = 2
		}
		if chroma == 1 {
			cy *= 2
		}
	}
	w, h := int((mw+1)*16)-int((left+right)*cx), int((mh+1)*16*(2-frame))-int((top+bottom)*cy)
	if r.err || w < 1 || h < 1 || w > 4096 || h > 4096 {
		return 0, 0, fmt.Errorf("unsupported SPS dimensions")
	}
	return w, h, nil
}
