package limaquic

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"

	"google.golang.org/protobuf/encoding/protowire"
)

// The private-dial paths, each the whole of an allowed :path.
const (
	pathSession = "/session"
	pathDial    = "/dial"
	pathGetHost = "/get-host"
)

// authorityMatches reports whether authority names sni, alone or with
// port, written exactly.
func authorityMatches(authority, sni string, port uint16) bool {
	if authority == sni {
		return true
	}
	return authority == net.JoinHostPort(sni, strconv.FormatUint(uint64(port), 10))
}

// readFirst reads the request body's first varint-delimited message and
// returns it whole, prefix included, and its message alone.
func readFirst(body io.Reader) (raw, message []byte, err error) {
	reader := byteReader{body}
	length, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, nil, err
	}
	message = make([]byte, length)
	if _, err := io.ReadFull(body, message); err != nil {
		return nil, nil, err
	}
	return append(binary.AppendUvarint(nil, length), message...), message, nil
}

// byteReader reads one byte at a time, so that reading the length prefix
// takes nothing past it.
type byteReader struct{ r io.Reader }

func (b byteReader) ReadByte() (byte, error) {
	var c [1]byte
	_, err := io.ReadFull(b.r, c[:])
	return c[0], err
}

var errMalformed = errors.New("malformed message")

// parseDialReq returns a DialReq's host (field 1) and port (field 2).
func parseDialReq(m []byte) (host string, port uint64, err error) {
	for len(m) > 0 {
		num, typ, n := protowire.ConsumeTag(m)
		if n < 0 {
			return "", 0, errMalformed
		}
		m = m[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, n := protowire.ConsumeBytes(m)
			if n < 0 {
				return "", 0, errMalformed
			}
			host, m = string(v), m[n:]
		case num == 2 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(m)
			if n < 0 {
				return "", 0, errMalformed
			}
			port, m = v, m[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, m)
			if n < 0 {
				return "", 0, errMalformed
			}
			m = m[n:]
		}
	}
	return host, port, nil
}

// parseGetHostReq returns a GetHostReq's host (field 1).
func parseGetHostReq(m []byte) (string, error) {
	host, _, err := parseDialReq(m)
	return host, err
}
