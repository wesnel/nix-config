package gitstage

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // Git's index checksum, not a security hash.
	"encoding/binary"
	"fmt"
)

// The index format, version 2 and 3, with SHA-1 object names: Git's
// Documentation/gitformat-index.txt.

const (
	entryFixed      = 62 // ten 32-bit stat fields, the object name, the flags
	extendedFlag    = 0x4000
	assumeValidFlag = 0x8000
	nameMask        = 0x0fff
)

type entry struct {
	raw   []byte // the whole entry, padding included, written back as read
	name  string
	mode  uint32
	oid   [sha1.Size]byte
	flags uint16
}

func (e entry) stage() int { return int(e.flags>>12) & 3 }

type index struct {
	version uint32
	entries []entry
}

// parseIndex reads an index, refusing what Install cannot keep intact:
// version 4's compressed names, a checksum that is not SHA-1, and the
// extensions that change what the entries mean (a split or sparse index,
// or any whose name says a reader must understand it). The other
// extensions are caches Git rebuilds, and Install drops them.
func parseIndex(data []byte) (index, error) {
	if len(data) < 12+sha1.Size || !bytes.Equal(data[:4], []byte("DIRC")) {
		return index{}, unsupported("no index header")
	}
	body, sum := data[:len(data)-sha1.Size], data[len(data)-sha1.Size:]
	if s := sha1.Sum(body); !bytes.Equal(s[:], sum) { //nolint:gosec // Git's checksum.
		return index{}, unsupported("its checksum is not SHA-1, or it is damaged")
	}
	ix := index{version: binary.BigEndian.Uint32(data[4:8])}
	if ix.version != 2 && ix.version != 3 {
		return index{}, unsupported(fmt.Sprintf("version %d", ix.version))
	}
	count := binary.BigEndian.Uint32(data[8:12])
	at := 12
	for range count {
		e, err := parseEntry(body, at, ix.version)
		if err != nil {
			return index{}, err
		}
		ix.entries = append(ix.entries, e)
		at += len(e.raw)
	}
	if err := checkExtensions(body, at); err != nil {
		return index{}, err
	}
	return ix, nil
}

// unsupported is why parseIndex refuses an index.
func unsupported(what string) error {
	return fmt.Errorf("the index is unsupported: %s", what)
}

// parseEntry reads the entry at offset at of body, an index of version
// version without its checksum.
func parseEntry(body []byte, at int, version uint32) (entry, error) {
	if at+entryFixed > len(body) {
		return entry{}, unsupported("an entry runs past its end")
	}
	e := entry{mode: binary.BigEndian.Uint32(body[at+24 : at+28]), flags: binary.BigEndian.Uint16(body[at+60 : at+62])}
	copy(e.oid[:], body[at+40:at+60])
	fixed := entryFixed
	if e.flags&extendedFlag != 0 {
		if version < 3 {
			return entry{}, unsupported("extended flags in version 2")
		}
		fixed += 2
	}
	start := at + fixed
	nameLen := bytes.IndexByte(body[min(start, len(body)):], 0)
	if nameLen < 0 {
		return entry{}, unsupported("an entry's name is unterminated")
	}
	if l := int(e.flags & nameMask); l != nameMask && l != nameLen {
		return entry{}, unsupported("an entry's name length disagrees with its flags")
	}
	size := (fixed + nameLen + 8) &^ 7
	if at+size > len(body) {
		return entry{}, unsupported("an entry runs past its end")
	}
	e.name = string(body[start : start+nameLen])
	e.raw = body[at : at+size]
	return e, nil
}

// checkExtensions refuses an extension, from offset at of body to its end,
// that changes what the entries mean or is malformed.
func checkExtensions(body []byte, at int) error {
	for at < len(body) {
		if at+8 > len(body) {
			return unsupported("a truncated extension")
		}
		sig := string(body[at : at+4])
		size := int(binary.BigEndian.Uint32(body[at+4 : at+8]))
		if size < 0 || at+8+size > len(body) {
			return unsupported("an extension runs past its end")
		}
		if sig[0] < 'A' || sig[0] > 'Z' || sig == "link" || sig == "sdir" {
			return unsupported(fmt.Sprintf("extension %q", sig))
		}
		at += 8 + size
	}
	return nil
}

// writeIndex is an index of entries, in order, without extensions.
func writeIndex(version uint32, entries []entry) []byte {
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, version)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(entries))) //nolint:gosec // bounded by indexLimit.
	for _, e := range entries {
		b.Write(e.raw)
	}
	sum := sha1.Sum(b.Bytes()) //nolint:gosec // Git's checksum.
	b.Write(sum[:])
	return b.Bytes()
}
