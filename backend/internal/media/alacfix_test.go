package media

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRepairALACTerminatorsPatchesMissingEndTag(t *testing.T) {
	bodyEndBit := 31
	packet := minimalUncompressedALACPacket(false)
	input := minimalALACMP4(packet)

	repaired, patched, err := repairALACTerminators(input)
	if err != nil {
		t.Fatal(err)
	}
	if patched != 1 {
		t.Fatalf("patched packets = %d, want 1", patched)
	}
	if bytes.Equal(repaired, input) {
		t.Fatal("repairALACTerminators did not change malformed ALAC packet")
	}

	repairedPacket := mdatPayload(t, repaired)
	if got := showBits(repairedPacket, bodyEndBit, 3); got != 7 {
		t.Fatalf("end tag bits = %03b, want 111", got)
	}
	for bit := bodyEndBit + 3; bit < len(repairedPacket)*8; bit++ {
		if got := showBits(repairedPacket, bit, 1); got != 0 {
			t.Fatalf("padding bit %d = %d, want 0", bit, got)
		}
	}
}

func mdatPayload(t *testing.T, data []byte) []byte {
	t.Helper()
	for pos := 0; pos <= len(data)-8; {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		if size < 8 || pos+size > len(data) {
			t.Fatalf("invalid MP4 box at offset %d", pos)
		}
		if string(data[pos+4:pos+8]) == "mdat" {
			return data[pos+8 : pos+size]
		}
		pos += size
	}
	t.Fatal("mdat box not found")
	return nil
}

func TestRepairALACTerminatorsLeavesExistingEndTagAlone(t *testing.T) {
	packet := minimalUncompressedALACPacket(true)
	input := minimalALACMP4(packet)

	repaired, patched, err := repairALACTerminators(input)
	if err != nil {
		t.Fatal(err)
	}
	if patched != 0 {
		t.Fatalf("patched packets = %d, want 0", patched)
	}
	if !bytes.Equal(repaired, input) {
		t.Fatal("repairALACTerminators changed packet that already has TYPE_END")
	}
}

func TestRepairALACTerminatorsSkipsNonALAC(t *testing.T) {
	packet := minimalUncompressedALACPacket(false)
	input := minimalAudioMP4("mp4a", packet)

	repaired, patched, err := repairALACTerminators(input)
	if err != nil {
		t.Fatal(err)
	}
	if patched != 0 {
		t.Fatalf("patched packets = %d, want 0", patched)
	}
	if !bytes.Equal(repaired, input) {
		t.Fatal("repairALACTerminators changed non-ALAC audio")
	}
}

func minimalUncompressedALACPacket(withEndTag bool) []byte {
	b := newTestBitWriter()
	b.write(0, 3)    // mono element
	b.write(0, 4)    // unused
	b.write(0, 12)   // unused
	b.write(0, 1)    // has explicit size: false, use maxSamplesPerFrame
	b.write(0, 2)    // extra bits
	b.write(1, 1)    // not compressed
	b.write(0xa5, 8) // one 8-bit sample
	if withEndTag {
		b.write(7, 3)
	} else {
		b.write(0, 3)
	}
	b.write(0x2a, 6) // dirty tail/padding; repair should zero this after TYPE_END
	return b.bytes()
}

func minimalALACMP4(packet []byte) []byte {
	return minimalAudioMP4("alac", packet)
}

func minimalAudioMP4(sampleEntryType string, packet []byte) []byte {
	ftyp := mp4Box("ftyp", append([]byte("M4A \x00\x00\x00\x00M4A "), []byte("mp42")...))
	packetOffset := uint32(len(ftyp) + 8)
	mdat := mp4Box("mdat", packet)
	moov := minimalMoov(sampleEntryType, len(packet), packetOffset)
	return append(append(ftyp, mdat...), moov...)
}

func minimalMoov(sampleEntryType string, sampleSize int, packetOffset uint32) []byte {
	stsd := mp4Box("stsd", append(fullBoxHeader(), append(u32(1), sampleEntry(sampleEntryType)...)...))
	stsz := mp4Box("stsz", append(fullBoxHeader(), append(append(u32(0), u32(1)...), u32(uint32(sampleSize))...)...))
	stscEntry := append(append(u32(1), u32(1)...), u32(1)...)
	stsc := mp4Box("stsc", append(fullBoxHeader(), append(u32(1), stscEntry...)...))
	stco := mp4Box("stco", append(fullBoxHeader(), append(u32(1), u32(packetOffset)...)...))
	stbl := mp4Box("stbl", append(append(append(stsd, stsz...), stsc...), stco...))
	minf := mp4Box("minf", stbl)
	hdlr := mp4Box("hdlr", append(append(fullBoxHeader(), u32(0)...), append([]byte("soun"), make([]byte, 12)...)...))
	mdia := mp4Box("mdia", append(hdlr, minf...))
	tkhdBody := append(fullBoxHeader(), make([]byte, 8)...)
	tkhdBody = append(tkhdBody, u32(1)...)
	tkhdBody = append(tkhdBody, u32(0)...)
	tkhd := mp4Box("tkhd", tkhdBody)
	trak := mp4Box("trak", append(tkhd, mdia...))
	return mp4Box("moov", trak)
}

func sampleEntry(sampleEntryType string) []byte {
	payload := make([]byte, 28)
	if sampleEntryType == "alac" {
		payload = append(payload, alacConfigAtom()...)
	}
	return mp4Box(sampleEntryType, payload)
}

func alacConfigAtom() []byte {
	cookie := make([]byte, 28)
	binary.BigEndian.PutUint32(cookie[4:8], 1) // maxSamplesPerFrame
	cookie[9] = 8                              // sample size
	cookie[10] = 40                            // rice history multiplier
	cookie[11] = 10                            // rice initial history
	cookie[12] = 14                            // rice limit
	cookie[13] = 1                             // channels
	return mp4Box("alac", cookie)
}

func mp4Box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBoxHeader() []byte {
	return []byte{0, 0, 0, 0}
}

func u32(v uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}

type testBitWriter struct {
	buf []byte
	pos int
}

func newTestBitWriter() *testBitWriter {
	return &testBitWriter{}
}

func (w *testBitWriter) write(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.pos%8 == 0 {
			w.buf = append(w.buf, 0)
		}
		if (v>>uint(i))&1 == 1 {
			w.buf[w.pos/8] |= 1 << uint(7-(w.pos&7))
		}
		w.pos++
	}
}

func (w *testBitWriter) bytes() []byte {
	return append([]byte(nil), w.buf...)
}

func showBits(buf []byte, bitOffset, n int) uint32 {
	var out uint32
	for i := 0; i < n; i++ {
		bp := bitOffset + i
		out = (out << 1) | uint32((buf[bp/8]>>uint(7-(bp&7)))&1)
	}
	return out
}
