package dns

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// Just enough of RFC 1035 for a caching forwarder: the question (cache key),
// EDNS(0) UDP size and DO bit of a query, the TTL fields and rcode of an
// answer.

const (
	headerLen  = 12
	typeOPT    = 41
	rcodeOK    = 0
	rcodeNX    = 3
	rcodeFail  = 2
	flagQR     = 0x8000
	flagTC     = 0x0200
	flagRA     = 0x0080
	opcodeMask = 0x7800
)

var errMsg = errors.New("dns: malformed message")

// query is what the forwarder needs from a client query.
type query struct {
	key     string // lower-case name, type, class, DO and CD bits
	name    string // lower-case name without the trailing dot ("" = root)
	qEnd    int    // offset just past the question
	maxUDP  int    // largest UDP answer the client takes (512 without EDNS)
	opcode0 bool   // standard query
}

// parseQuery checks a query with exactly one question.
func parseQuery(b []byte) (query, error) {
	if len(b) < headerLen {
		return query{}, errMsg
	}
	flags := binary.BigEndian.Uint16(b[2:])
	if flags&flagQR != 0 || binary.BigEndian.Uint16(b[4:]) != 1 {
		return query{}, errMsg
	}
	name, off, err := readName(b, headerLen)
	if err != nil || off+4 > len(b) {
		return query{}, errMsg
	}
	qtype, qclass := binary.BigEndian.Uint16(b[off:]), binary.BigEndian.Uint16(b[off+2:])
	q := query{qEnd: off + 4, maxUDP: 512, opcode0: flags&opcodeMask == 0,
		name: strings.TrimSuffix(strings.ToLower(name), ".")}
	do := false
	// The OPT record, if any, is in the additional section.
	off = q.qEnd
	an, ns, ar := binary.BigEndian.Uint16(b[6:]), binary.BigEndian.Uint16(b[8:]), binary.BigEndian.Uint16(b[10:])
	for i := 0; i < int(an)+int(ns)+int(ar); i++ {
		rr, next, err := readRR(b, off)
		if err != nil {
			return query{}, err
		}
		if rr.typ == typeOPT {
			q.maxUDP = max(512, int(rr.class))
			do = b[rr.ttlOff+2]&0x80 != 0
		}
		off = next
	}
	cd := flags&0x0010 != 0
	q.key = strings.ToLower(name) + "/" + strconv.Itoa(int(qtype)) + "/" + strconv.Itoa(int(qclass)) +
		"/" + strconv.FormatBool(do) + "/" + strconv.FormatBool(cd)
	return q, nil
}

// readName decodes a (possibly compressed) name starting at off and returns
// it with the offset just past it in the message.
func readName(b []byte, off int) (string, int, error) {
	var sb strings.Builder
	end, jumps := -1, 0
	for {
		if off >= len(b) {
			return "", 0, errMsg
		}
		l := int(b[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			if sb.Len() == 0 {
				sb.WriteByte('.')
			}
			return sb.String(), end, nil
		case l&0xC0 == 0xC0:
			if off+1 >= len(b) || jumps > 16 {
				return "", 0, errMsg
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(b[off:]) & 0x3FFF)
			jumps++
		case l&0xC0 != 0:
			return "", 0, errMsg
		default:
			if off+1+l > len(b) || sb.Len()+l > 255 {
				return "", 0, errMsg
			}
			sb.Write(b[off+1 : off+1+l])
			sb.WriteByte('.')
			off += 1 + l
		}
	}
}

type rr struct {
	typ, class uint16
	ttlOff     int
}

func readRR(b []byte, off int) (rr, int, error) {
	_, off, err := readName(b, off)
	if err != nil || off+10 > len(b) {
		return rr{}, 0, errMsg
	}
	r := rr{typ: binary.BigEndian.Uint16(b[off:]), class: binary.BigEndian.Uint16(b[off+2:]), ttlOff: off + 4}
	end := off + 10 + int(binary.BigEndian.Uint16(b[off+8:]))
	if end > len(b) {
		return rr{}, 0, errMsg
	}
	return r, end, nil
}

// answerTTLs returns the offsets of every TTL field (except OPT) and the
// smallest TTL; n is the number of such records.
func answerTTLs(b []byte) (offs []int, minTTL uint32, err error) {
	if len(b) < headerLen || binary.BigEndian.Uint16(b[4:]) != 1 {
		return nil, 0, errMsg
	}
	_, off, err := readName(b, headerLen)
	if err != nil {
		return nil, 0, err
	}
	off += 4
	n := int(binary.BigEndian.Uint16(b[6:])) + int(binary.BigEndian.Uint16(b[8:])) + int(binary.BigEndian.Uint16(b[10:]))
	minTTL = ^uint32(0)
	for range n {
		r, next, err := readRR(b, off)
		if err != nil {
			return nil, 0, err
		}
		if r.typ != typeOPT {
			offs = append(offs, r.ttlOff)
			minTTL = min(minTTL, binary.BigEndian.Uint32(b[r.ttlOff:]))
		}
		off = next
	}
	return offs, minTTL, nil
}

const typeA = 1

// answerA returns the question name (lower case, no trailing dot) and the
// IPv4 addresses in the answer section with the smallest of their TTLs
// (CNAME chains included: the addresses belong to the asked name).
func answerA(b []byte) (name string, ips []netip.Addr, minTTL uint32, err error) {
	if len(b) < headerLen || binary.BigEndian.Uint16(b[4:]) != 1 {
		return "", nil, 0, errMsg
	}
	name, off, err := readName(b, headerLen)
	if err != nil || off+4 > len(b) {
		return "", nil, 0, errMsg
	}
	off += 4
	minTTL = ^uint32(0)
	for range int(binary.BigEndian.Uint16(b[6:])) {
		r, next, err := readRR(b, off)
		if err != nil {
			return "", nil, 0, err
		}
		if r.typ == typeA && r.class == 1 && next-r.ttlOff == 10 {
			ips = append(ips, netip.AddrFrom4([4]byte(b[next-4:next])))
			minTTL = min(minTTL, binary.BigEndian.Uint32(b[r.ttlOff:]))
		}
		off = next
	}
	if len(ips) == 0 {
		minTTL = 0
	}
	return strings.TrimSuffix(strings.ToLower(name), "."), ips, minTTL, nil
}

func rcode(b []byte) int { return int(b[3] & 0x0F) }

func truncated(b []byte) bool { return binary.BigEndian.Uint16(b[2:])&flagTC != 0 }

// servfail builds a SERVFAIL answer to the query (header + question).
func servfail(q []byte, qEnd int) []byte { return emptyAnswer(q, qEnd, rcodeFail) }

// emptyAnswer builds an answer with rcode rc and no records to the query
// (header + question).
func emptyAnswer(q []byte, qEnd int, rc uint16) []byte {
	r := make([]byte, qEnd)
	copy(r, q[:qEnd])
	flags := binary.BigEndian.Uint16(q[2:])
	flags = flagQR | flags&(opcodeMask|0x0100) | flagRA | rc // keep opcode and RD
	binary.BigEndian.PutUint16(r[2:], flags)
	binary.BigEndian.PutUint16(r[4:], 1)
	binary.BigEndian.PutUint16(r[6:], 0)
	binary.BigEndian.PutUint16(r[8:], 0)
	binary.BigEndian.PutUint16(r[10:], 0)
	return r
}

// sameQuestion reports whether the answer is for the question of the query.
func sameQuestion(q []byte, qEnd int, a []byte) bool {
	return len(a) >= qEnd && binary.BigEndian.Uint16(a[4:]) == 1 &&
		strings.EqualFold(string(a[headerLen:qEnd]), string(q[headerLen:qEnd]))
}
