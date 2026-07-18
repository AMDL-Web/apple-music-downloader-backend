package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zhaarey/go-mp4tag"
)

// topLevelMP4Box is the small amount of ISO BMFF structure needed to decide
// whether metadata can be rewritten without copying the media payload.
type topLevelMP4Box struct {
	typ        string
	start      int64
	size       int64
	headerSize int64
	// extendsToEOF records the ISO BMFF size==0 encoding. The resolved size
	// above is still useful for structural checks, but go-mp4tag treats the raw
	// zero as a zero-byte box and recursively seeks back to the same offset.
	// Keep the original encoding so we can fail closed instead of invoking that
	// parser on an unsafe source layout.
	extendsToEOF bool
}

var errInvalidTopLevelMP4 = errors.New("invalid top-level MP4 box layout")

var errUnsupportedMP4TagLayout = errors.New("MP4 box layout is unsupported by metadata writer")

// writeMP4Tags keeps go-mp4tag as the single implementation of the iTunes tag
// format, but avoids its whole-file temporary copy for the layout emitted by
// our ffmpeg flatten step. Unusual MP4 layouts retain the library's original,
// fully compatible rewrite path.
func writeMP4Tags(path string, tags *mp4tag.MP4Tags) error {
	usedFastPath, err := writeMP4TagsFast(path, tags)
	if err != nil {
		return err
	}
	if usedFastPath {
		return nil
	}
	return writeMP4TagsLegacy(path, tags)
}

func writeMP4TagsLegacy(path string, tags *mp4tag.MP4Tags) error {
	track, err := mp4tag.Open(path)
	if err != nil {
		return err
	}
	defer track.Close()
	return track.Write(tags, []string{})
}

// writeMP4TagsFast rewrites only the final moov box when every media-data box
// precedes it. It returns used=false without touching path when the layout (or
// the metadata shell) is structurally unsuitable, allowing writeMP4Tags to use
// the exact legacy fallback. Actual filesystem I/O failures are returned: a
// full or unhealthy temp volume must not trigger an even larger legacy copy.
//
// go-mp4tag's output is deliberately not reimplemented here. Instead, a small
// metadata shell containing the original ftyp and moov plus an empty mdat is
// passed through go-mp4tag. Because the shell's mdat precedes its ilst, the
// library leaves all stco values unchanged. The resulting moov is therefore
// byte-for-byte the same metadata rewrite the library would have produced for
// the real mdat-before-moov file (apart from irrelevant map iteration order),
// while the potentially multi-gigabyte mdat is never read or copied.
func writeMP4TagsFast(path string, tags *mp4tag.MP4Tags) (used bool, err error) {
	source, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer func() {
		// Some filesystems report delayed writeback failures only when the file
		// is closed. Do not let a successfully copied/truncated moov proceed to
		// finalization when Close says the staging file was not committed.
		if closeErr := source.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	stat, err := source.Stat()
	if err != nil {
		return false, err
	}
	boxes, err := readTopLevelMP4Boxes(source, stat.Size())
	if err != nil {
		if errors.Is(err, errInvalidTopLevelMP4) {
			return false, nil
		}
		return false, err
	}
	ftyp, moov, ok := metadataTailLayout(boxes, stat.Size())
	if !ok {
		// go-mp4tag only understands regular 32-bit box headers. Its recursive
		// parser does not advance for a raw size of zero, and interprets the
		// extended-size marker (one) as the complete box size. A normal layout
		// can still use the compatible legacy rewrite, but never pass these
		// encodings to it. Eligible tail-moov layouts may contain an extended
		// mdat because the metadata shell deliberately replaces that box.
		for _, box := range boxes {
			if box.extendsToEOF || box.headerSize != 8 {
				return false, fmt.Errorf("%w: top-level %s uses a non-32-bit size", errUnsupportedMP4TagLayout, box.typ)
			}
		}
		return false, nil
	}

	shell, err := os.CreateTemp(filepath.Dir(path), ".mp4tag-shell-*")
	if err != nil {
		return false, err
	}
	shellPath := shell.Name()
	defer os.Remove(shellPath)

	if _, err = io.CopyN(shell, io.NewSectionReader(source, ftyp.start, ftyp.size), ftyp.size); err != nil {
		shell.Close()
		return false, err
	}
	// A real top-level mdat is required by go-mp4tag's parser. Its contents and
	// size are immaterial for a tail-moov rewrite, so an empty box is enough.
	if _, err = shell.Write([]byte{0, 0, 0, 8, 'm', 'd', 'a', 't'}); err != nil {
		shell.Close()
		return false, err
	}
	if _, err = io.CopyN(shell, io.NewSectionReader(source, moov.start, moov.size), moov.size); err != nil {
		shell.Close()
		return false, err
	}
	if err = shell.Close(); err != nil {
		return false, err
	}

	// All potentially fallible tag parsing/serialization happens against the
	// disposable shell. The real staging file remains untouched until a complete
	// replacement moov has been produced and validated below.
	if err = writeMP4TagsLegacy(shellPath, tags); err != nil {
		if isMP4TagFormatError(err) {
			return false, nil
		}
		return false, err
	}

	taggedShell, err := os.Open(shellPath)
	if err != nil {
		return false, err
	}
	defer taggedShell.Close()
	taggedStat, err := taggedShell.Stat()
	if err != nil {
		return false, err
	}
	taggedBoxes, err := readTopLevelMP4Boxes(taggedShell, taggedStat.Size())
	if err != nil {
		return false, fmt.Errorf("parse tagged metadata shell: %w", err)
	}
	_, taggedMoov, ok := metadataTailLayout(taggedBoxes, taggedStat.Size())
	if !ok {
		return false, errors.New("go-mp4tag produced an invalid metadata shell layout")
	}

	// From this point the source may be partially changed if the filesystem
	// reports an error, so signal used=true and never retry through the legacy
	// whole-file writer on top of a possibly incomplete moov.
	if _, err = source.Seek(moov.start, io.SeekStart); err != nil {
		return true, err
	}
	if _, err = io.CopyN(source, io.NewSectionReader(taggedShell, taggedMoov.start, taggedMoov.size), taggedMoov.size); err != nil {
		return true, err
	}
	if err = source.Truncate(moov.start + taggedMoov.size); err != nil {
		return true, err
	}
	return true, nil
}

func readTopLevelMP4Boxes(r io.ReaderAt, fileSize int64) ([]topLevelMP4Box, error) {
	if fileSize < 0 {
		return nil, errInvalidTopLevelMP4
	}
	var boxes []topLevelMP4Box
	for offset := int64(0); offset < fileSize; {
		if fileSize-offset < 8 {
			return nil, errInvalidTopLevelMP4
		}
		var header [16]byte
		if _, err := r.ReadAt(header[:8], offset); err != nil {
			return nil, err
		}
		rawSize := binary.BigEndian.Uint32(header[:4])
		size := int64(rawSize)
		headerSize := int64(8)
		switch size {
		case 0:
			size = fileSize - offset
		case 1:
			if fileSize-offset < 16 {
				return nil, errInvalidTopLevelMP4
			}
			if _, err := r.ReadAt(header[8:16], offset+8); err != nil {
				return nil, err
			}
			extended := binary.BigEndian.Uint64(header[8:16])
			if extended > uint64(^uint64(0)>>1) {
				return nil, errInvalidTopLevelMP4
			}
			size = int64(extended)
			headerSize = 16
		}
		if size < headerSize || size > fileSize-offset {
			return nil, errInvalidTopLevelMP4
		}
		boxes = append(boxes, topLevelMP4Box{
			typ:          string(header[4:8]),
			start:        offset,
			size:         size,
			headerSize:   headerSize,
			extendsToEOF: rawSize == 0,
		})
		offset += size
	}
	return boxes, nil
}

func isMP4TagFormatError(err error) bool {
	var (
		missing     *mp4tag.ErrBoxNotPresent
		unsupported *mp4tag.ErrUnsupportedFtyp
		stco        *mp4tag.ErrInvalidStcoSize
		magic       *mp4tag.ErrInvalidMagic
	)
	return errors.As(err, &missing) || errors.As(err, &unsupported) || errors.As(err, &stco) || errors.As(err, &magic) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func metadataTailLayout(boxes []topLevelMP4Box, fileSize int64) (ftyp, moov topLevelMP4Box, ok bool) {
	var (
		ftypCount int
		moovCount int
		mdatCount int
	)
	for _, box := range boxes {
		switch box.typ {
		case "ftyp":
			ftyp, ftypCount = box, ftypCount+1
		case "moov":
			moov, moovCount = box, moovCount+1
		case "mdat":
			mdatCount++
		}
	}
	// go-mp4tag only understands regular 32-bit ftyp/moov headers. The source
	// may use an extended mdat header; the shell intentionally normalizes that.
	if ftypCount != 1 || moovCount != 1 || mdatCount == 0 || ftyp.start != 0 || ftyp.headerSize != 8 || ftyp.extendsToEOF || moov.headerSize != 8 || moov.extendsToEOF || moov.start+moov.size != fileSize {
		return topLevelMP4Box{}, topLevelMP4Box{}, false
	}
	for _, box := range boxes {
		if box.typ == "mdat" && box.start > moov.start {
			return topLevelMP4Box{}, topLevelMP4Box{}, false
		}
	}
	return ftyp, moov, true
}
