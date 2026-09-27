package collab

import (
	"encoding/binary"
	"errors"
)

// y-protocols message encoding (lib0: unsigned LEB128 varints, length-
// prefixed byte arrays and strings). Frames on the WebSocket already say
// whether they're sync or awareness, so payloads are y-protocols messages
// without the y-websocket outer type.

// Sync message types (y-protocols/sync).
const (
	syncStep1  = 0 // payload: state vector
	syncStep2  = 1 // payload: update
	syncUpdate = 2 // payload: update
)

var errShort = errors.New("collab: truncated message")

type reader struct {
	b   []byte
	off int
}

func (r *reader) uint() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.off:])
	if n <= 0 {
		return 0, errShort
	}
	r.off += n
	return v, nil
}

func (r *reader) bytes() ([]byte, error) {
	n, err := r.uint()
	if err != nil {
		return nil, err
	}
	if uint64(len(r.b)-r.off) < n {
		return nil, errShort
	}
	out := r.b[r.off : r.off+int(n)]
	r.off += int(n)
	return out, nil
}

func appendUint(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

func appendBytes(b, data []byte) []byte {
	return append(appendUint(b, uint64(len(data))), data...)
}

// parseSync splits a sync message into its type and payload.
func parseSync(msg []byte) (typ uint64, payload []byte, err error) {
	r := &reader{b: msg}
	if typ, err = r.uint(); err != nil {
		return 0, nil, err
	}
	payload, err = r.bytes()
	return typ, payload, err
}

func syncMsg(typ uint64, payload []byte) []byte {
	return appendBytes(appendUint(make([]byte, 0, len(payload)+8), typ), payload)
}

// awarenessEntry is one client's presence state.
type awarenessEntry struct {
	Client uint64
	Clock  uint64
	State  string // JSON, "null" when the client left
}

func parseAwareness(msg []byte) ([]awarenessEntry, error) {
	r := &reader{b: msg}
	n, err := r.uint()
	if err != nil {
		return nil, err
	}
	if n > 1024 {
		return nil, errors.New("collab: too many awareness entries")
	}
	out := make([]awarenessEntry, 0, n)
	for range n {
		var e awarenessEntry
		if e.Client, err = r.uint(); err != nil {
			return nil, err
		}
		if e.Clock, err = r.uint(); err != nil {
			return nil, err
		}
		s, err := r.bytes()
		if err != nil {
			return nil, err
		}
		e.State = string(s)
		out = append(out, e)
	}
	return out, nil
}

func encodeAwareness(entries []awarenessEntry) []byte {
	b := appendUint(nil, uint64(len(entries)))
	for _, e := range entries {
		b = appendUint(b, e.Client)
		b = appendUint(b, e.Clock)
		b = appendBytes(b, []byte(e.State))
	}
	return b
}
