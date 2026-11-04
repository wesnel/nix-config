package limaquic

import (
	"context"
	"encoding/binary"
	"io"
	"net"

	"golang.org/x/net/dns/dnsmessage"
)

// The privatedial client resolves its server's name with net.DefaultResolver
// and takes no resolver of its own, and real DNS reaches outside the
// machine. So TestMain points the default resolver at this one, which
// answers an A query for a name in loopbackNames with 127.0.0.1, any other
// question about such a name with no answer, and every other name with
// NXDOMAIN.
var loopbackNames = map[string]bool{
	"us.quic.connect-endpoint.ngrok.com.": true,
}

func installFakeDNS() {
	net.DefaultResolver.PreferGo = true
	net.DefaultResolver.Dial = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go serveDNS(server)
		return client, nil
	}
}

// serveDNS answers the length-prefixed queries of DNS over a stream, as
// the Go resolver sends them over a connection that is not a PacketConn.
func serveDNS(conn net.Conn) {
	defer conn.Close()
	for {
		var length [2]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(length[:]))
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		answer, err := answerDNS(query)
		if err != nil {
			return
		}
		framed := binary.BigEndian.AppendUint16(nil, uint16(len(answer)))
		if _, err := conn.Write(append(framed, answer...)); err != nil {
			return
		}
	}
}

func answerDNS(query []byte) ([]byte, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(query)
	if err != nil {
		return nil, err
	}
	question, err := parser.Question()
	if err != nil {
		return nil, err
	}
	known := loopbackNames[question.Name.String()]
	reply := dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true, RecursionAvailable: true}
	if !known {
		reply.RCode = dnsmessage.RCodeNameError
	}
	builder := dnsmessage.NewBuilder(nil, reply)
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(question); err != nil {
		return nil, err
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	if known && question.Type == dnsmessage.TypeA {
		resource := dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}
		if err := builder.AResource(resource, dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}); err != nil {
			return nil, err
		}
	}
	return builder.Finish()
}
