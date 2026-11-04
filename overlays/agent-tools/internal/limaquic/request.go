package limaquic

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/wesnel/agent-tools/internal/limaegress"
)

// The private-dial paths, each the whole of an allowed :path.
const (
	pathSession = "/session"
	pathDial    = "/dial"
	pathGetHost = "/get-host"
)

// maxFirstMessage bounds the first message the broker reads; the client's
// are far smaller.
const maxFirstMessage = 64 << 10

// authorityMatches reports whether authority names sni, alone or with
// port, written exactly.
func authorityMatches(authority, sni string, port uint16) bool {
	if authority == sni {
		return true
	}
	return authority == net.JoinHostPort(sni, strconv.FormatUint(uint64(port), 10))
}

// readFirst reads the request body's first varint-delimited message, of
// at most maxFirstMessage bytes, and returns it whole, prefix included, and
// its message alone. It grows its buffer as bytes come, so a declared
// length that never arrives costs nothing.
func readFirst(body io.Reader) (raw, message []byte, err error) {
	length, err := binary.ReadUvarint(byteReader{body})
	if err != nil {
		return nil, nil, err
	}
	if length > maxFirstMessage {
		return nil, nil, fmt.Errorf("a %d-byte first message exceeds %d bytes", length, maxFirstMessage)
	}
	var buf bytes.Buffer
	buf.Write(binary.AppendUvarint(nil, length))
	prefix := buf.Len()
	if _, err := io.CopyN(&buf, body, int64(length)); err != nil { //nolint:gosec // length is at most maxFirstMessage
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, nil, err
	}
	raw = buf.Bytes()
	return raw, raw[prefix:], nil
}

// atEnd returns nil if body ends here, and an error if any byte follows or
// the read fails.
func atEnd(body io.Reader) error {
	var b [1]byte
	n, err := io.ReadFull(body, b[:])
	if n > 0 {
		return errors.New("bytes follow the message")
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// byteReader reads one byte at a time, so that reading the length prefix
// takes nothing past it.
type byteReader struct{ r io.Reader }

func (b byteReader) ReadByte() (byte, error) {
	var c [1]byte
	_, err := io.ReadFull(b.r, c[:])
	return c[0], err
}

// A field, as scan finds it: for the bytes type, its bytes; for the
// varint type, its value.
type field struct {
	num   protowire.Number
	typ   protowire.Type
	bytes []byte
	value uint64
}

// scan returns the fields of message m in order, or an error if m does not
// parse.
func scan(m []byte) ([]field, error) {
	var fields []field
	for len(m) > 0 {
		num, typ, n := protowire.ConsumeTag(m)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		m = m[n:]
		f := field{num: num, typ: typ}
		switch typ {
		case protowire.BytesType:
			f.bytes, n = protowire.ConsumeBytes(m)
		case protowire.VarintType:
			f.value, n = protowire.ConsumeVarint(m)
		default:
			n = protowire.ConsumeFieldValue(num, typ, m)
		}
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		m = m[n:]
		fields = append(fields, f)
	}
	return fields, nil
}

// A rule says what a message's field must be: its type, and how many times
// it may appear; max 0 is any number.
type rule struct {
	typ      protowire.Type
	min, max int
}

// check scans m and holds each field to its number's rule, refusing any
// number without one, so no field the broker did not judge can reach a
// parser that reads it differently, as protobuf's last-one-wins does a
// repeated field.
func check(m []byte, rules map[protowire.Number]rule) ([]field, error) {
	fields, err := scan(m)
	if err != nil {
		return nil, err
	}
	count := map[protowire.Number]int{}
	for _, f := range fields {
		r, ok := rules[f.num]
		if !ok {
			return nil, fmt.Errorf("field %d is not allowed", f.num)
		}
		if f.typ != r.typ {
			return nil, fmt.Errorf("field %d has wire type %d, want %d", f.num, f.typ, r.typ)
		}
		count[f.num]++
		if r.max > 0 && count[f.num] > r.max {
			return nil, fmt.Errorf("field %d appears more than %d times", f.num, r.max)
		}
	}
	for num, r := range rules {
		if count[num] < r.min {
			return nil, fmt.Errorf("field %d is missing", num)
		}
	}
	return fields, nil
}

var (
	// DialReq{1: host, 2: port, 3: metadata entries, 4: session_req}.
	dialReqRules = map[protowire.Number]rule{
		1: {typ: protowire.BytesType, min: 1, max: 1},
		2: {typ: protowire.VarintType, min: 1, max: 1},
		3: {typ: protowire.BytesType},
		4: {typ: protowire.BytesType, max: 1},
	}
	// GetHostReq{1: host}.
	getHostReqRules = map[protowire.Number]rule{
		1: {typ: protowire.BytesType, min: 1, max: 1},
	}
	// SessionReq{1: client_version, 2: metadata entries}.
	sessionReqRules = map[protowire.Number]rule{
		1: {typ: protowire.BytesType, max: 1},
		2: {typ: protowire.BytesType},
	}
)

var errHost = errors.New("the host is not a lowercase LDH name or an IP literal")

// parseDialReq returns a DialReq's host and port, refusing any encoding
// that is not exactly one host, one port and the optional fields.
func parseDialReq(m []byte) (host string, port uint64, err error) {
	fields, err := check(m, dialReqRules)
	if err != nil {
		return "", 0, err
	}
	for _, f := range fields {
		switch f.num {
		case 1:
			host = string(f.bytes)
		case 2:
			port = f.value
		}
	}
	if !utf8.ValidString(host) || limaegress.CheckHost(host) != nil {
		return "", 0, errHost
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port %d is out of range", port)
	}
	return host, port, nil
}

// parseGetHostReq returns a GetHostReq's host, its one field.
func parseGetHostReq(m []byte) (string, error) {
	fields, err := check(m, getHostReqRules)
	if err != nil {
		return "", err
	}
	host := string(fields[0].bytes)
	if !utf8.ValidString(host) || limaegress.CheckHost(host) != nil {
		return "", errHost
	}
	return host, nil
}

// checkSessionReq refuses a SessionReq with any field but its client
// version, once, and metadata entries.
func checkSessionReq(m []byte) error {
	fields, err := check(m, sessionReqRules)
	if err != nil {
		return err
	}
	for _, f := range fields {
		if f.num == 1 && !utf8.Valid(f.bytes) {
			return errors.New("the client version is not UTF-8")
		}
	}
	return nil
}
