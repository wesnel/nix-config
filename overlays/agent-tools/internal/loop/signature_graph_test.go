package loop

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func poisonCommitGraphParent(t *testing.T, dir, commit, originalParent, replacement string) {
	t.Helper()
	raw := gitIn(t, dir, "cat-file", "commit", commit)
	gitIn(t, dir, "commit-graph", "write", "--reachable")
	path := filepath.Join(dir, ".git", "objects", "info", "commit-graph")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Git's v1 SHA-1 graph stores sorted OIDs and 36-byte commit records.
	if len(data) < 28 || string(data[:4]) != "CGPH" || data[4] != 1 || data[5] != 1 || data[7] != 0 {
		t.Fatal("unexpected fixture commit-graph format")
	}
	chunks := map[string]int{}
	for i := 0; i <= int(data[6]); i++ {
		at := 8 + i*12
		if at+12 > len(data)-20 {
			t.Fatal("short fixture chunk table")
		}
		offset := binary.BigEndian.Uint64(data[at+4 : at+12])
		if offset > uint64(len(data)-20) {
			t.Fatal("fixture chunk outside graph")
		}
		chunks[string(data[at:at+4])] = int(offset)
	}
	fanout, ok := chunks["OIDF"]
	if !ok || fanout+1024 > len(data)-20 {
		t.Fatal("missing fixture OID fanout")
	}
	count := int(binary.BigEndian.Uint32(data[fanout+1020 : fanout+1024]))
	oids, ok := chunks["OIDL"]
	if !ok || oids+count*20 > len(data)-20 {
		t.Fatal("missing fixture OID lookup")
	}
	commits, ok := chunks["CDAT"]
	if !ok || commits+count*36 > len(data)-20 {
		t.Fatal("missing fixture commit records")
	}
	position := func(oid string) int {
		t.Helper()
		want, err := hex.DecodeString(oid)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < count; i++ {
			if bytes.Equal(data[oids+i*20:oids+(i+1)*20], want) {
				return i
			}
		}
		t.Fatalf("fixture OID missing from graph: %s", oid)
		return -1
	}
	at := commits + position(commit)*36 + 20
	if int(binary.BigEndian.Uint32(data[at:at+4])) != position(originalParent) {
		t.Fatal("fixture parent mismatch")
	}
	binary.BigEndian.PutUint32(data[at:at+4], uint32(position(replacement)))
	checksum := sha1.Sum(data[:len(data)-20])
	copy(data[len(data)-20:], checksum[:])
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, dir, "cat-file", "commit", commit); got != raw {
		t.Fatal("changing graph altered raw signed commit")
	}
}
