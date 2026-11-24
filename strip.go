// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/loov/logicx/internal/record"

	"howett.net/plist"
)

// Within a channel strip chunk: its index among the strips of its kind,
// which a spare strip keeps when it is filled. The strip's own ID is the
// first of the three IDs ending the chunk.
const (
	channelStripIndex     = channelStripKind + 2
	channelStripIndexCopy = 0x80
)

// Within an environment object: its own number, a flag Logic set on one of
// the objects it made, and the object's vertical place in the environment,
// which Logic steps by environmentStep for each new track.
const (
	environmentObject   = 16
	environmentSelected = 0x50 // 0x01
	environmentKind     = 0x51
	environmentY        = 0x52
	environmentStep     = 0x40
	environmentGroup    = 0x14
)

// AddTrack adds a track below source's playing a channel strip of its own, a
// copy of source's with its instrument, effects and settings, named name, as
// Logic's New Track does. The strip is one of the project's spare instrument
// strips, those Logic keeps without a track or a plug-in, such as the strip
// of a deleted track; when none is left, a new one is made, as Logic does,
// after the last track's strip. The tracks below move down a row, taking
// their regions along. ProjectData's values are not updated; see
// [ProjectData.Refresh].
func (p *ProjectData) AddTrack(source *Track, name string) (ArrangeTrack, error) {
	if source.chunk == nil || source.environment == nil || source.trak == nil {
		return ArrangeTrack{}, fmt.Errorf("logicx: track %q has no strip, environment object or arrange track to copy", source.Name)
	}
	if source.Kind != TrackKindInstrument {
		return ArrangeTrack{}, fmt.Errorf("logicx: track %q is not a software instrument", source.Name)
	}
	spare := p.spareStrip()
	if spare == nil {
		var err error
		if spare, err = p.insertSpareStrip(); err != nil {
			return ArrangeTrack{}, err
		}
	}
	d := source.chunk.Data
	if len(d) < channelStripIndexCopy+2 || len(d) < channelStripRecord+channelStripRecordSize+3*routeIDSize ||
		len(spare.chunk.Data) < channelStripIndexCopy+2 {
		return ArrangeTrack{}, errors.New("logicx: channel strip too short to copy")
	}
	oldObject := chunkSequenceID(source.environment).sequence
	object := p.nextEnvironmentObject(oldObject)
	id := newUUID()

	// The strip: source's settings under the spare's name and index.
	strip := bytes.Clone(d)
	s := spare.chunk.Data
	copy(strip[channelStripKind:channelStripIndex+2], s[channelStripKind:channelStripIndex+2])
	copy(strip[channelStripRecord:channelStripRecord+16], s[channelStripRecord:channelStripRecord+16])
	copy(strip[channelStripIndexCopy:channelStripIndexCopy+2], s[channelStripIndexCopy:channelStripIndexCopy+2])
	copy(strip[len(strip)-3*routeIDSize:], id)

	// The plug-ins, each with an ID of its own.
	var plugins []*Chunk
	for _, c := range p.Chunks {
		if c.Type != pluginChunk || !bytes.Equal(c.Header[4:8], pluginVariant) ||
			binary.LittleEndian.Uint16(c.Header[14:]) != source.strip {
			continue
		}
		copied := &Chunk{Type: c.Type, Header: c.Header, Data: bytes.Clone(c.Data)}
		binary.LittleEndian.PutUint16(copied.Header[14:], spare.strip)
		renewPluginID(copied.Data)
		var err error
		if copied.Data, err = retargetInstrumentID(copied.Data, oldObject, object); err != nil {
			return ArrangeTrack{}, fmt.Errorf("logicx: plug-in of %q: %w", source.Name, err)
		}
		plugins = append(plugins, copied)
	}

	// The environment object naming the strip, under the track's name.
	env := &Chunk{Type: source.environment.Type, Header: source.environment.Header, Data: bytes.Clone(source.environment.Data)}
	binary.LittleEndian.PutUint32(env.Header[10:], object)
	binary.LittleEndian.PutUint32(env.Data[environmentObject:], object)
	if len(env.Data) > environmentY+2 {
		binary.LittleEndian.PutUint16(env.Data[environmentY:], p.lowestEnvironmentY(env.Data)+environmentStep)
	}
	copy(env.Data[len(env.Data)-16:], id)
	env.Data[environmentSelected] &^= 0x01
	if err := clearEnvironmentLink(env.Data); err != nil {
		return ArrangeTrack{}, err
	}
	if err := setEnvironmentStrip(env.Data, spare.strip); err != nil {
		return ArrangeTrack{}, err
	}
	if name != "" {
		var err error
		if env.Data, err = renameEnvironment(env.Data, name); err != nil {
			return ArrangeTrack{}, err
		}
	}

	// The arrange track, below source's.
	var below *ArrangeTrack
	for i := range p.ArrangeTracks {
		if p.ArrangeTracks[i].chunk == source.trak {
			below = &p.ArrangeTracks[i]
		}
	}
	if below == nil {
		return ArrangeTrack{}, fmt.Errorf("logicx: track %q is not in the Tracks area", source.Name)
	}
	added, err := p.DuplicateArrangeTrack(below)
	if err != nil {
		return ArrangeTrack{}, err
	}
	binary.LittleEndian.PutUint32(added.chunk.Data[arrangeTrackEnvironment:], object)
	// Logic leaves this bit on a track New Track made; Logic 13 shows a
	// track without it with only its solo and input monitoring buttons.
	added.chunk.Data[arrangeTrackSelectedAlso] |= 0x40
	added.object, added.Track = object, spare.Name

	spare.chunk.Data = strip
	at := slices.Index(p.Chunks, spare.chunk)
	p.Chunks = slices.Insert(p.Chunks, at+1, plugins...)
	last := slices.IndexFunc(p.Chunks, func(c *Chunk) bool { return c == source.environment })
	for i, c := range p.Chunks {
		if c.Type == environmentChunk && chunkSequenceID(c).group == environmentGroup {
			last = i
		}
	}
	p.Chunks = slices.Insert(p.Chunks, last+1, env)
	if err := p.registerObject(chordSequenceID{environmentGroup, object}); err != nil {
		return ArrangeTrack{}, err
	}
	return added, nil
}

// spareStrip returns the first instrument strip without a plug-in or an ID
// of its own, which Logic gives a strip when a track plays it.
func (p *ProjectData) spareStrip() *Track {
	used := make(map[uint16]bool)
	for _, u := range p.AudioUnits {
		used[u.strip] = true
	}
	for i := range p.Tracks {
		t := &p.Tracks[i]
		d := t.chunk.Data
		if t.Kind == TrackKindInstrument && !used[t.strip] && strings.HasPrefix(t.Name, "Inst ") &&
			len(d) >= 3*routeIDSize && allZero(d[len(d)-3*routeIDSize:len(d)-2*routeIDSize]) {
			return t
		}
	}
	return nil
}

// insertSpareStrip makes a spare instrument strip, as Logic does when it has
// none left: in place of the last instrument strip, the click's, which moves
// along with every strip after it. Strips are numbered by their place, so
// the plug-ins of the strips that move, and the environment objects naming
// them, are renumbered too.
func (p *ProjectData) insertSpareStrip() (*Track, error) {
	var click *Chunk
	for _, c := range p.Chunks {
		if isChannelStrip(c) && len(c.Data) > channelStripIndexCopy+2 && trackKind(c.Data) == TrackKindInstrument {
			click = c
		}
	}
	if click == nil {
		return nil, errors.New("logicx: no instrument strip to make a spare after")
	}
	at := binary.LittleEndian.Uint16(click.Header[14:])
	index := binary.LittleEndian.Uint16(click.Data[channelStripIndex:])
	if err := p.countStrip(); err != nil {
		return nil, err
	}

	for _, c := range p.Chunks {
		if (isChannelStrip(c) || c.Type == pluginChunk && bytes.Equal(c.Header[4:8], pluginVariant)) &&
			binary.LittleEndian.Uint16(c.Header[14:]) >= at {
			binary.LittleEndian.PutUint16(c.Header[14:], binary.LittleEndian.Uint16(c.Header[14:])+1)
		}
		if c.Type == environmentChunk && chunkSequenceID(c).group == environmentGroup &&
			len(c.Data) > environmentKind && c.Data[environmentKind] == 0x11 {
			if strip, ok := environmentStrip(c.Data); ok && strip >= at {
				if err := setEnvironmentStrip(c.Data, strip+1); err != nil {
					return nil, err
				}
			}
		}
	}
	setInstrumentIndex(click.Data, index+1)

	// The spare is filled from the source strip; only its place, index and
	// name are its own.
	spare := &Chunk{Type: click.Type, Header: click.Header, Data: bytes.Clone(click.Data)}
	binary.LittleEndian.PutUint16(spare.Header[14:], at)
	setInstrumentIndex(spare.Data, index)
	binary.LittleEndian.PutUint16(spare.Data[channelStripIndexCopy:], index)
	clear(spare.Data[len(spare.Data)-3*routeIDSize : len(spare.Data)-2*routeIDSize])
	p.Chunks = slices.Insert(p.Chunks, slices.Index(p.Chunks, click), spare)
	return &Track{chunk: spare, strip: at, Name: fmt.Sprintf("Inst %d", index+1)}, nil
}

// The strip list chunk counts the strips: all of them, and those of each
// kind, by kind code from stripListKinds. It ends with an entry for each
// strip, stripListEntry.
const (
	channelStripGroup    = 0x0e
	channelStripSequence = 0x24
	instrumentKindCode   = 3 // TrackKindInstrument's code; see trackKinds

	stripListChunk = "AuCn"
	stripListTotal = 26
	stripListKinds = 28
)

var stripListEntry = []byte{1, 0, 0, 0}

// countStrip adds an instrument strip to the strip list's counts.
func (p *ProjectData) countStrip() error {
	for _, c := range p.Chunks {
		if c.Type != stripListChunk || chunkSequenceID(c) != (chordSequenceID{channelStripGroup, channelStripSequence}) {
			continue
		}
		d := c.Data
		kind := stripListKinds + 2*instrumentKindCode
		if len(d) < kind+2 {
			break
		}
		total := int(binary.LittleEndian.Uint16(d[stripListTotal:]))
		if len(d) < stripListKinds+4*total || !bytes.Equal(d[len(d)-4*total:], bytes.Repeat(stripListEntry, total)) {
			return errors.New("logicx: strip list does not end with an entry for each strip")
		}
		binary.LittleEndian.PutUint16(d[stripListTotal:], uint16(total+1))
		binary.LittleEndian.PutUint16(d[kind:], binary.LittleEndian.Uint16(d[kind:])+1)
		c.Data = append(d, stripListEntry...)
		return nil
	}
	return errors.New("logicx: no strip list")
}

// setInstrumentIndex sets an instrument strip's index and the name Logic
// gives it from that, Inst and the index counted from one.
func setInstrumentIndex(data []byte, index uint16) {
	binary.LittleEndian.PutUint16(data[channelStripIndex:], index)
	name := data[channelStripRecord+1 : channelStripRecord+16]
	clear(name)
	copy(name, fmt.Sprintf("Inst %d", index+1))
}

// nextEnvironmentObject returns the first number after after that no
// environment object has, as Logic picks one for a new track.
func (p *ProjectData) nextEnvironmentObject(after uint32) uint32 {
	used := make(map[uint32]bool)
	for _, c := range p.Chunks {
		if id := chunkSequenceID(c); c.Type == environmentChunk && id.group == environmentGroup {
			used[id.sequence] = true
		}
	}
	next := after + 4
	for used[next] {
		next += 4
	}
	return next
}

// lowestEnvironmentY returns the lowest place among the objects like like:
// of its length, and of its kind in byte 0x51.
func (p *ProjectData) lowestEnvironmentY(like []byte) uint16 {
	var y uint16
	for _, c := range p.Chunks {
		if c.Type == environmentChunk && len(c.Data) == len(like) && c.Data[environmentY-1] == like[environmentY-1] {
			y = max(y, binary.LittleEndian.Uint16(c.Data[environmentY:]))
		}
	}
	return y
}

// setEnvironmentStrip sets the strip an environment object names, stored
// plus one after its name; see environmentStrip.
func setEnvironmentStrip(data []byte, strip uint16) error {
	var name string
	if !record.Decode(data, record.String16(environmentNameLength, &name)) {
		return errors.New("logicx: environment object has no name")
	}
	at := environmentNameLength + 2 + len(name)
	at += at & 1
	if at+2 > len(data) {
		return errors.New("logicx: environment object too short")
	}
	binary.LittleEndian.PutUint16(data[at:], strip+1)
	return nil
}

// environmentLink is where, counted from the strip an environment object
// names, Logic 13 keeps a value it shares between the selected track's
// object and the Stereo Out's; any other object holds zero. A track whose
// object holds it as well is left out of the mixer.
const environmentLink = 218

// clearEnvironmentLink zeroes an environment object's environmentLink.
func clearEnvironmentLink(data []byte) error {
	var name string
	if !record.Decode(data, record.String16(environmentNameLength, &name)) {
		return errors.New("logicx: environment object has no name")
	}
	at := environmentNameLength + 2 + len(name)
	at += at&1 + environmentLink
	if at+4 > len(data) {
		return errors.New("logicx: environment object too short")
	}
	clear(data[at : at+4])
	return nil
}

// renameEnvironment returns an environment object renamed, keeping what
// follows the name at the next even offset.
func renameEnvironment(data []byte, name string) ([]byte, error) {
	var old string
	if !record.Decode(data, record.String16(environmentNameLength, &old)) {
		return nil, errors.New("logicx: environment object has no name")
	}
	if len(name) > 0xff {
		return nil, fmt.Errorf("logicx: track name %q is too long", name)
	}
	aligned := func(n int) int { end := environmentNameLength + 2 + n; return end + end&1 }
	out := slices.Clone(data[:environmentNameLength])
	out = binary.LittleEndian.AppendUint16(out, uint16(len(name)))
	out = append(out, name...)
	if len(out)&1 != 0 {
		out = append(out, 0)
	}
	return append(out, data[aligned(len(old)):]...), nil
}

// newUUID returns a new time-based UUID, as Logic gives every object it
// makes. Logic 13 does not take a strip with a random (version 4) ID for one
// in use: its track shows no mixer controls and the strip no fader.
func newUUID() []byte {
	return timeUUID(uuidTime())
}

// renewPluginID gives a plug-in chunk a new instance ID. Logic ends the
// chunk with it, or with it and four zero bytes; it is recognised by the
// version and variant bits of a UUID, and left alone when not found.
func renewPluginID(data []byte) {
	for _, end := range []int{len(data), len(data) - 4} {
		if end < 16 || end < len(data) && !allZero(data[end:]) {
			continue
		}
		id := data[end-16 : end]
		if version := id[6] >> 4; version >= 1 && version <= 5 && id[8]&0xc0 == 0x80 {
			copy(id, newUUID())
			return
		}
	}
}

// retargetInstrumentID rewrites a plug-in state archive's kGInstIDKey
// values, which name the track's environment object, from old to object. The
// archive is a binary property list, a keyed archive in which equal integers
// share one object; that object is overwritten in place, as Logic does, so
// the new number must fit its width. A chunk without such an archive is
// returned as it is.
func retargetInstrumentID(data []byte, old, object uint32) ([]byte, error) {
	at := bytes.Index(data, []byte("bplist00"))
	if at < 4 || !bytes.Contains(data[at:], []byte("kGInstIDKey")) {
		return data, nil
	}
	size := int(binary.LittleEndian.Uint32(data[at-4:]))
	if size < 40 || at+size > len(data) {
		return nil, errors.New("state archive overruns its chunk")
	}
	list := data[at : at+size]
	var archive struct {
		Objects []any `plist:"$objects"`
	}
	if _, err := plist.Unmarshal(list, &archive); err != nil {
		return nil, fmt.Errorf("state archive: %w", err)
	}
	if !slices.ContainsFunc(archive.Objects, func(o any) bool {
		dict, ok := o.(map[string]any)
		return ok && toUint64(dict["kGInstIDKey"]) == uint64(old)
	}) {
		return data, nil
	}

	trailer := list[len(list)-32:]
	offsetSize, count := int(trailer[6]), int(binary.BigEndian.Uint64(trailer[8:]))
	table := int(binary.BigEndian.Uint64(trailer[24:]))
	if offsetSize < 1 || offsetSize > 8 || table+count*offsetSize > len(list)-32 {
		return nil, errors.New("state archive has a bad offset table")
	}
	out := bytes.Clone(data)
	patched := 0
	for i := range count {
		var offset int
		for _, b := range list[table+i*offsetSize : table+(i+1)*offsetSize] {
			offset = offset<<8 | int(b)
		}
		if offset >= len(list)-32 || list[offset]&0xf0 != 0x10 {
			continue
		}
		width := 1 << (list[offset] & 0x0f)
		if width > 8 || offset+1+width > len(list) {
			continue
		}
		var v uint64
		for _, b := range list[offset+1 : offset+1+width] {
			v = v<<8 | uint64(b)
		}
		if v != uint64(old) {
			continue
		}
		if width < 8 && uint64(object) >= 1<<(8*width) {
			return nil, fmt.Errorf("environment object %d does not fit the archive's %d-byte integer", object, width)
		}
		for k := range width {
			out[at+offset+1+k] = byte(uint64(object) >> (8 * (width - 1 - k)))
		}
		patched++
	}
	if patched == 0 {
		return nil, errors.New("state archive names the track but holds no integer for it")
	}
	return out, nil
}

func toUint64(v any) uint64 {
	switch v := v.(type) {
	case uint64:
		return v
	case int64:
		return uint64(v)
	}
	return 0
}
