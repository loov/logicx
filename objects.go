// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync/atomic"
	"time"
)

// The song chunk keeps two tables of the project's objects, each sorted by
// the group and number of the chunks holding them and ended by an entry for
// objectTablesEnd. The first gives each object a time-based UUID, made when
// the object was; the second the time it last changed, in the same units. An
// object Logic does not find there, such as a track's environment object,
// is left out: the track shows no mixer controls and its strip no fader.
const (
	objectEntrySize = 24
	objectTimeSize  = 16
)

var objectTablesEnd = []byte{0x23, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}

// registerObject gives the object numbered id a new UUID and change time in
// the song's tables, as Logic does when it makes one.
func (p *ProjectData) registerObject(id chordSequenceID) error {
	var song *Chunk
	for _, c := range p.Chunks {
		if c.Type == songChunk {
			song = c
		}
	}
	if song == nil {
		return errors.New("logicx: no song chunk to register an object in")
	}
	end := bytes.Index(song.Data, objectTablesEnd)
	if end < 16 {
		return errors.New("logicx: song chunk has no object table")
	}
	times := bytes.Index(song.Data[end+len(objectTablesEnd):], objectTablesEnd)
	if times < 0 {
		return errors.New("logicx: song chunk has no object time table")
	}
	times += end + len(objectTablesEnd)

	now := uuidTime()
	uuid := timeUUID(now)
	stamp := binary.LittleEndian.AppendUint64(nil, now)
	// The time table comes after the UUID table, so it is changed first.
	data, err := setObjectEntry(song.Data, times-8, objectTimeSize, id, stamp)
	if err != nil {
		return err
	}
	if data, err = setObjectEntry(data, end-16, objectEntrySize, id, uuid); err != nil {
		return err
	}
	song.Data = data
	return nil
}

// setObjectEntry sets id's value in the table whose end entry is at last,
// entries size bytes long, each a value and then a group and a number: it
// fills id's entry when there is one, and inserts one in order otherwise.
func setObjectEntry(data []byte, last, size int, id chordSequenceID, value []byte) ([]byte, error) {
	key := func(at int) uint64 {
		return uint64(binary.LittleEndian.Uint32(data[at+size-8:]))<<32 | uint64(binary.LittleEndian.Uint32(data[at+size-4:]))
	}
	want := uint64(id.group)<<32 | uint64(id.sequence)
	at := last
	for at-size >= 0 && key(at-size) >= want {
		at -= size
		if key(at) == want {
			out := bytes.Clone(data)
			copy(out[at:], value)
			return out, nil
		}
		if key(at) > key(at+size) {
			return nil, errors.New("logicx: song object table is out of order")
		}
	}
	entry := append(bytes.Clone(value), make([]byte, 8)...)
	binary.LittleEndian.PutUint32(entry[size-8:], id.group)
	binary.LittleEndian.PutUint32(entry[size-4:], id.sequence)
	out := append(bytes.Clone(data[:at]), entry...)
	return append(out, data[at:]...), nil
}

// lastUUIDTime keeps uuidTime increasing when it is called within one tick.
var lastUUIDTime atomic.Uint64

// uuidTime returns the time now in the units of a time-based UUID, 100
// nanoseconds since the Gregorian calendar began, later than any it
// returned before.
func uuidTime() uint64 {
	now := uint64(time.Now().UnixNano()/100) + 0x01b21dd213814000
	for {
		last := lastUUIDTime.Load()
		next := max(now, last+1)
		if lastUUIDTime.CompareAndSwap(last, next) {
			return next
		}
	}
}

// timeUUID returns a time-based (version 1) UUID for t, with a random clock
// sequence and node.
func timeUUID(t uint64) []byte {
	id := make([]byte, 16)
	binary.BigEndian.PutUint32(id[0:], uint32(t))
	binary.BigEndian.PutUint16(id[4:], uint16(t>>32))
	binary.BigEndian.PutUint16(id[6:], uint16(t>>48)&0x0fff|0x1000)
	rand.Read(id[8:])
	id[8] = id[8]&0x3f | 0x80
	return id
}
