package media

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type alacBitReader struct {
	buf []byte
	pos int
}

var errALACBitEOF = errors.New("alac bit reader EOF")

func newALACBitReader(buf []byte) *alacBitReader {
	return &alacBitReader{buf: buf}
}

func (r *alacBitReader) left() int {
	return len(r.buf)*8 - r.pos
}

func (r *alacBitReader) read(n int) (uint32, error) {
	if n == 0 {
		return 0, nil
	}
	if n < 0 || r.pos+n > len(r.buf)*8 {
		return 0, errALACBitEOF
	}
	var out uint32
	for i := 0; i < n; i++ {
		bp := r.pos + i
		out = (out << 1) | uint32((r.buf[bp/8]>>uint(7-(bp&7)))&1)
	}
	r.pos += n
	return out, nil
}

func (r *alacBitReader) show(n int) (uint32, error) {
	saved := r.pos
	out, err := r.read(n)
	r.pos = saved
	return out, err
}

func (r *alacBitReader) skip(n int) error {
	if n < 0 || r.pos+n > len(r.buf)*8 {
		return errALACBitEOF
	}
	r.pos += n
	return nil
}

func (r *alacBitReader) readSigned(n int) (int32, error) {
	out, err := r.read(n)
	if err != nil {
		return 0, err
	}
	if out&(1<<uint(n-1)) != 0 {
		return int32(out) - int32(1<<uint(n)), nil
	}
	return int32(out), nil
}

func (r *alacBitReader) unary09() (uint32, error) {
	for count := uint32(0); count < 9; count++ {
		bit, err := r.read(1)
		if err != nil {
			return 0, err
		}
		if bit == 0 {
			return count, nil
		}
	}
	return 9, nil
}

type alacRepairParams struct {
	maxSamplesPerFrame uint32
	sampleSize         uint8
	riceHistoryMult    uint8
	riceInitialHistory uint8
	riceLimit          uint8
	channels           uint8
}

type mp4Atom struct {
	bodyOff int
	endOff  int
}

type alacPacketLoc struct {
	offset int64
	size   int
}

type alacTrackData struct {
	params alacRepairParams
	locs   []alacPacketLoc
}

func repairALACTerminators(song []byte) ([]byte, int, error) {
	tracks, err := findALACRepairTracks(song)
	if err != nil {
		return nil, 0, err
	}
	if len(tracks) == 0 {
		return append([]byte(nil), song...), 0, nil
	}

	repaired := append([]byte(nil), song...)
	patched := 0
	for _, track := range tracks {
		for _, loc := range track.locs {
			if loc.size <= 0 || loc.offset < 0 || loc.offset+int64(loc.size) > int64(len(repaired)) {
				continue
			}
			packet := repaired[loc.offset : loc.offset+int64(loc.size)]
			bodyEnd := findALACBodyEndBit(packet, &track.params)
			if bodyEnd < 0 || bodyEnd == loc.size*8 {
				continue
			}
			reader := newALACBitReader(packet)
			if err := reader.skip(bodyEnd); err != nil {
				continue
			}
			if reader.left() >= 3 {
				tag, err := reader.show(3)
				if err == nil && tag == 7 {
					continue
				}
			}
			if patchALACTerminator(repaired, loc.offset, loc.size, bodyEnd) {
				patched++
			}
		}
	}
	return repaired, patched, nil
}

func findALACBodyEndBit(packet []byte, params *alacRepairParams) int {
	reader := newALACBitReader(packet)
	channelsUsed := 0
	lastEnd := -1
	for reader.left() >= 3 {
		channels, isEnd, err := scanALACElement(reader, params)
		if err != nil {
			return -1
		}
		if isEnd {
			return reader.pos
		}
		lastEnd = reader.pos
		channelsUsed += channels
		if channelsUsed >= int(params.channels) {
			return lastEnd
		}
	}
	return lastEnd
}

func scanALACElement(reader *alacBitReader, params *alacRepairParams) (int, bool, error) {
	elem, err := reader.read(3)
	if err != nil {
		return 0, false, err
	}
	if elem == 7 {
		return 0, true, nil
	}
	if elem > 1 && elem != 3 {
		return 0, false, fmt.Errorf("unsupported ALAC element tag %d", elem)
	}

	channels := 1
	if elem == 1 {
		channels = 2
	}
	if err := reader.skip(16); err != nil {
		return 0, false, err
	}
	hasSize, err := reader.read(1)
	if err != nil {
		return 0, false, err
	}
	extraBitsRaw, err := reader.read(2)
	if err != nil {
		return 0, false, err
	}
	extraBits := int(extraBitsRaw) << 3
	bps := int(params.sampleSize) - extraBits + channels - 1
	if bps < 1 || bps > 32 {
		return 0, false, fmt.Errorf("bad ALAC bits-per-sample %d", bps)
	}
	notCompressed, err := reader.read(1)
	if err != nil {
		return 0, false, err
	}
	outputSamples := params.maxSamplesPerFrame
	if hasSize != 0 {
		outputSamples, err = reader.read(32)
		if err != nil {
			return 0, false, err
		}
	}
	if outputSamples == 0 || outputSamples > params.maxSamplesPerFrame {
		return 0, false, fmt.Errorf("bad ALAC output samples %d", outputSamples)
	}

	if notCompressed != 0 {
		need := int(outputSamples) * channels * int(params.sampleSize)
		if reader.left() < need {
			return 0, false, errALACBitEOF
		}
		return channels, false, reader.skip(need)
	}

	if _, err := reader.read(8); err != nil {
		return 0, false, err
	}
	if _, err := reader.read(8); err != nil {
		return 0, false, err
	}
	riceHistoryMultipliers := make([]uint32, channels)
	for channel := 0; channel < channels; channel++ {
		if _, err := reader.read(4); err != nil {
			return 0, false, err
		}
		lpcQuant, err := reader.read(4)
		if err != nil {
			return 0, false, err
		}
		riceHistoryMultiplier, err := reader.read(3)
		if err != nil {
			return 0, false, err
		}
		lpcOrder, err := reader.read(5)
		if err != nil {
			return 0, false, err
		}
		if lpcQuant == 0 || lpcOrder >= params.maxSamplesPerFrame {
			return 0, false, errors.New("bad ALAC LPC header")
		}
		for i := uint32(0); i < lpcOrder; i++ {
			if _, err := reader.readSigned(16); err != nil {
				return 0, false, err
			}
		}
		riceHistoryMultipliers[channel] = riceHistoryMultiplier
	}
	if extraBits != 0 {
		need := int(outputSamples) * channels * extraBits
		if reader.left() < need {
			return 0, false, errALACBitEOF
		}
		if err := reader.skip(need); err != nil {
			return 0, false, err
		}
	}
	for channel := 0; channel < channels; channel++ {
		riceHistoryMultiplier := (riceHistoryMultipliers[channel] * uint32(params.riceHistoryMult)) / 4
		if err := skipALACRice(reader, int(outputSamples), bps, riceHistoryMultiplier, params); err != nil {
			return 0, false, err
		}
	}
	return channels, false, nil
}

func skipALACRice(reader *alacBitReader, samples, bps int, riceHistoryMultiplier uint32, params *alacRepairParams) error {
	history := uint32(params.riceInitialHistory)
	signModifier := uint32(0)
	limit := int(params.riceLimit)
	maxIterations := samples*4 + 100
	iterations := 0
	for i := 0; i < samples; {
		iterations++
		if iterations > maxIterations {
			return errors.New("ALAC rice runaway")
		}
		k := alacLog2((history >> 9) + 3)
		if k > limit {
			k = limit
		}
		x, err := decodeALACScalar(reader, k, bps)
		if err != nil {
			return err
		}
		x += signModifier
		signModifier = 0
		if x > 0xffff {
			history = 0xffff
		} else {
			history = history + x*riceHistoryMultiplier - ((history * riceHistoryMultiplier) >> 9)
		}
		if history < 128 && i+1 < samples {
			k2 := 7 - alacLog2(history) + int((history+16)>>6)
			if k2 > limit {
				k2 = limit
			}
			blockSize, err := decodeALACScalar(reader, k2, 16)
			if err != nil {
				return err
			}
			if blockSize > 0 {
				if int(blockSize) >= samples-i {
					blockSize = uint32(samples - i - 1)
				}
				i += int(blockSize)
			}
			if blockSize <= 0xffff {
				signModifier = 1
			}
			history = 0
		}
		i++
	}
	return nil
}

func decodeALACScalar(reader *alacBitReader, k int, bps int) (uint32, error) {
	x, err := reader.unary09()
	if err != nil {
		return 0, err
	}
	if x > 8 {
		return reader.read(bps)
	}
	if k != 1 {
		extraBits, err := reader.show(k)
		if err != nil {
			return 0, err
		}
		x = (x << uint(k)) - x
		if extraBits > 1 {
			x += extraBits - 1
			if err := reader.skip(k); err != nil {
				return 0, err
			}
		} else {
			if err := reader.skip(k - 1); err != nil {
				return 0, err
			}
		}
	}
	return x, nil
}

func alacLog2(x uint32) int {
	result := 0
	for x > 1 {
		x >>= 1
		result++
	}
	return result
}

func patchALACTerminator(data []byte, offset int64, size int, bodyEndBit int) bool {
	totalBits := size * 8
	if bodyEndBit < 0 || bodyEndBit+3 > totalBits {
		return false
	}
	for i := 0; i < 3; i++ {
		bitPosition := bodyEndBit + i
		byteIndex := offset + int64(bitPosition/8)
		data[byteIndex] |= 1 << uint(7-(bitPosition&7))
	}
	padStart := bodyEndBit + 3
	byteIndex := offset + int64(padStart/8)
	bitInByte := padStart & 7
	if bitInByte != 0 {
		data[byteIndex] &= byte(0xff << uint(8-bitInByte))
		byteIndex++
	}
	for i := byteIndex; i < offset+int64(size); i++ {
		data[i] = 0
	}
	return true
}

func findALACRepairTracks(data []byte) ([]alacTrackData, error) {
	moov, ok := findMP4Child(data, 0, len(data), "moov")
	if !ok {
		return nil, errors.New("no moov atom in MP4/M4A")
	}
	var tracks []alacTrackData
	for _, trak := range findMP4Children(data, moov.bodyOff, moov.endOff, "trak") {
		mdia, ok := findMP4Child(data, trak.bodyOff, trak.endOff, "mdia")
		if !ok {
			continue
		}
		hdlr, ok := findMP4Child(data, mdia.bodyOff, mdia.endOff, "hdlr")
		if !ok || hdlr.endOff-hdlr.bodyOff < 12 || string(data[hdlr.bodyOff+8:hdlr.bodyOff+12]) != "soun" {
			continue
		}
		minf, ok := findMP4Child(data, mdia.bodyOff, mdia.endOff, "minf")
		if !ok {
			continue
		}
		stbl, ok := findMP4Child(data, minf.bodyOff, minf.endOff, "stbl")
		if !ok {
			continue
		}
		stsd, ok := findMP4Child(data, stbl.bodyOff, stbl.endOff, "stsd")
		if !ok || stsd.endOff-stsd.bodyOff < 16 {
			continue
		}
		entryCount := binary.BigEndian.Uint32(data[stsd.bodyOff+4 : stsd.bodyOff+8])
		if entryCount == 0 {
			continue
		}
		entryStart := stsd.bodyOff + 8
		entrySize := int(binary.BigEndian.Uint32(data[entryStart : entryStart+4]))
		if entrySize < 8 || entryStart+entrySize > stsd.endOff {
			continue
		}
		if string(data[entryStart+4:entryStart+8]) != "alac" {
			continue
		}
		sampleEntry := mp4Atom{bodyOff: entryStart + 8, endOff: entryStart + entrySize}
		params, err := extractALACRepairParams(data, sampleEntry)
		if err != nil {
			return nil, err
		}
		locs, err := readALACPacketLocations(data, stbl)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, alacTrackData{params: params, locs: locs})
	}
	return tracks, nil
}

func extractALACRepairParams(data []byte, sampleEntry mp4Atom) (alacRepairParams, error) {
	childStart := sampleEntry.bodyOff + 28
	if childStart > sampleEntry.endOff {
		return alacRepairParams{}, errors.New("ALAC sample entry too short")
	}
	if cfg, ok := findMP4Child(data, childStart, sampleEntry.endOff, "alac"); ok {
		return parseALACRepairCookie(data, cfg)
	}
	if wave, ok := findMP4Child(data, childStart, sampleEntry.endOff, "wave"); ok {
		if cfg, ok := findMP4Child(data, wave.bodyOff, wave.endOff, "alac"); ok {
			return parseALACRepairCookie(data, cfg)
		}
	}
	return alacRepairParams{}, errors.New("missing ALAC config atom")
}

func parseALACRepairCookie(data []byte, cfg mp4Atom) (alacRepairParams, error) {
	if cfg.endOff-cfg.bodyOff < 24 {
		return alacRepairParams{}, errors.New("ALAC config atom too short")
	}
	cookie := data[cfg.bodyOff:cfg.endOff]
	params := alacRepairParams{
		maxSamplesPerFrame: binary.BigEndian.Uint32(cookie[4:8]),
		sampleSize:         cookie[9],
		riceHistoryMult:    cookie[10],
		riceInitialHistory: cookie[11],
		riceLimit:          cookie[12],
		channels:           cookie[13],
	}
	if params.maxSamplesPerFrame == 0 || params.sampleSize == 0 || params.channels == 0 {
		return alacRepairParams{}, errors.New("invalid ALAC config atom")
	}
	return params, nil
}

func readALACPacketLocations(data []byte, stbl mp4Atom) ([]alacPacketLoc, error) {
	stsz, ok := findMP4Child(data, stbl.bodyOff, stbl.endOff, "stsz")
	if !ok {
		return nil, errors.New("stsz atom missing")
	}
	stsc, ok := findMP4Child(data, stbl.bodyOff, stbl.endOff, "stsc")
	if !ok {
		return nil, errors.New("stsc atom missing")
	}
	stco, ok := findMP4Child(data, stbl.bodyOff, stbl.endOff, "stco")
	is64 := false
	if !ok {
		stco, ok = findMP4Child(data, stbl.bodyOff, stbl.endOff, "co64")
		if !ok {
			return nil, errors.New("stco/co64 atom missing")
		}
		is64 = true
	}

	if stsz.endOff-stsz.bodyOff < 12 {
		return nil, errors.New("stsz atom too short")
	}
	defaultSize := binary.BigEndian.Uint32(data[stsz.bodyOff+4 : stsz.bodyOff+8])
	sampleCount := int(binary.BigEndian.Uint32(data[stsz.bodyOff+8 : stsz.bodyOff+12]))
	sizes := make([]uint32, sampleCount)
	if defaultSize == 0 {
		if stsz.endOff-stsz.bodyOff < 12+sampleCount*4 {
			return nil, errors.New("stsz sample table truncated")
		}
		for i := 0; i < sampleCount; i++ {
			sizes[i] = binary.BigEndian.Uint32(data[stsz.bodyOff+12+i*4 : stsz.bodyOff+16+i*4])
		}
	} else {
		for i := range sizes {
			sizes[i] = defaultSize
		}
	}

	if stco.endOff-stco.bodyOff < 8 {
		return nil, errors.New("chunk offset atom too short")
	}
	chunkCount := int(binary.BigEndian.Uint32(data[stco.bodyOff+4 : stco.bodyOff+8]))
	chunkOffsets := make([]int64, chunkCount)
	pos := stco.bodyOff + 8
	entrySize := 4
	if is64 {
		entrySize = 8
	}
	if stco.endOff-pos < chunkCount*entrySize {
		return nil, errors.New("chunk offset table truncated")
	}
	for i := 0; i < chunkCount; i++ {
		if is64 {
			chunkOffsets[i] = int64(binary.BigEndian.Uint64(data[pos : pos+8]))
		} else {
			chunkOffsets[i] = int64(binary.BigEndian.Uint32(data[pos : pos+4]))
		}
		pos += entrySize
	}

	if stsc.endOff-stsc.bodyOff < 8 {
		return nil, errors.New("stsc atom too short")
	}
	runCount := int(binary.BigEndian.Uint32(data[stsc.bodyOff+4 : stsc.bodyOff+8]))
	if stsc.endOff-stsc.bodyOff < 8+runCount*12 {
		return nil, errors.New("stsc table truncated")
	}
	type stscRun struct {
		firstChunk      uint32
		samplesPerChunk uint32
	}
	runs := make([]stscRun, runCount)
	pos = stsc.bodyOff + 8
	for i := 0; i < runCount; i++ {
		runs[i] = stscRun{
			firstChunk:      binary.BigEndian.Uint32(data[pos : pos+4]),
			samplesPerChunk: binary.BigEndian.Uint32(data[pos+4 : pos+8]),
		}
		pos += 12
	}

	samplesPerChunk := make([]uint32, len(chunkOffsets))
	for i, run := range runs {
		nextFirstChunk := uint32(len(chunkOffsets)) + 1
		if i+1 < len(runs) {
			nextFirstChunk = runs[i+1].firstChunk
		}
		for chunk := run.firstChunk; chunk < nextFirstChunk && int(chunk-1) < len(samplesPerChunk); chunk++ {
			if chunk > 0 {
				samplesPerChunk[chunk-1] = run.samplesPerChunk
			}
		}
	}
	if len(runs) > 0 {
		for i := range samplesPerChunk {
			if samplesPerChunk[i] == 0 {
				samplesPerChunk[i] = runs[len(runs)-1].samplesPerChunk
			}
		}
	}

	locs := make([]alacPacketLoc, 0, len(sizes))
	sampleIndex := 0
	for chunkIndex, chunkOffset := range chunkOffsets {
		currentOffset := chunkOffset
		for i := 0; i < int(samplesPerChunk[chunkIndex]) && sampleIndex < len(sizes); i++ {
			size := int(sizes[sampleIndex])
			locs = append(locs, alacPacketLoc{offset: currentOffset, size: size})
			currentOffset += int64(size)
			sampleIndex++
		}
		if sampleIndex >= len(sizes) {
			break
		}
	}
	return locs, nil
}

func findMP4Child(data []byte, start, end int, typ string) (mp4Atom, bool) {
	children := findMP4Children(data, start, end, typ)
	if len(children) == 0 {
		return mp4Atom{}, false
	}
	return children[0], true
}

func findMP4Children(data []byte, start, end int, typ string) []mp4Atom {
	if start < 0 || end > len(data) || start > end {
		return nil
	}
	var out []mp4Atom
	for pos := start; pos <= end-8; {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		headerSize := 8
		if size == 1 {
			if pos+16 > end {
				return out
			}
			size64 := binary.BigEndian.Uint64(data[pos+8 : pos+16])
			if size64 > uint64(int(^uint(0)>>1)) {
				return out
			}
			size = int(size64)
			headerSize = 16
		} else if size == 0 {
			size = end - pos
		}
		if size < headerSize || pos+size > end {
			return out
		}
		if string(data[pos+4:pos+8]) == typ {
			out = append(out, mp4Atom{bodyOff: pos + headerSize, endOff: pos + size})
		}
		pos += size
	}
	return out
}
