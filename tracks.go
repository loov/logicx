// SPDX-License-Identifier: GPL-3.0-or-later

package logicx

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/loov/logicx/internal/record"
)

// AudioUnit is one plug-in instance in a channel strip. Slot is its position
// within its own chain: on the audio chain the instrument sits at slot zero
// and the inserts are numbered from one, while MIDI effects number from zero.
// Slots left empty have no record at all, so the numbering has gaps.
//
// Logic's built-in plug-ins carry Emagic's manufacturer code and leave Type
// and Subtype empty, so Name identifies them. Third-party plug-ins fill in the
// full Audio Unit component description.
type AudioUnit struct {
	Name         string
	Type         string
	Subtype      string
	Manufacturer string
	Setting      string
	Slot         uint16
	Offset       int
	strip        uint16
	midi         bool
	chunk        *Chunk
	// name and setting are Name and Setting as decoded, so that Save writes
	// them only when they were changed.
	name, setting string
}

// Builtin reports whether the plug-in ships with Logic rather than being an
// installed Audio Unit component.
func (a AudioUnit) Builtin() bool { return a.Manufacturer == emagic && a.Type == "" }

// Fingerprint returns the Audio Unit type, subtype, and manufacturer tuple.
func (a AudioUnit) Fingerprint() string {
	return a.Type + "/" + a.Subtype + "/" + a.Manufacturer
}

// TrackKind classifies a Logic channel strip.
type TrackKind string

const (
	// TrackKindAudio is an audio channel strip.
	TrackKindAudio TrackKind = "audio"
	// TrackKindInstrument is a software instrument channel strip.
	TrackKindInstrument TrackKind = "instrument"
	// TrackKindMaster is the master channel strip.
	TrackKindMaster TrackKind = "master"
	// TrackKindOutput is a physical output channel strip.
	TrackKindOutput TrackKind = "output"
	// TrackKindBus is a bus channel strip.
	TrackKindBus TrackKind = "bus"
	// TrackKindAux is an auxiliary channel strip.
	TrackKindAux TrackKind = "aux"
	// TrackKindInput is a physical input channel strip.
	TrackKindInput TrackKind = "input"
	// TrackKindUnknown is a channel strip whose descriptor is not recognized.
	TrackKindUnknown TrackKind = "unknown"
)

// Track contains a decoded channel strip, its mixer settings and its plug-in
// chain.
type Track struct {
	Name   string
	Kind   TrackKind
	Offset int
	Active bool
	// Volume is the fader as 8.24 fixed point: 90.0 is 0 dB, and Logic's
	// fader tops out at 127.0, about +6 dB. See [Track.VolumeDB].
	Volume uint32
	// Pan runs from -64, fully left, to 63, fully right.
	Pan             int8
	Mute            bool
	Solo            bool
	InputMonitoring bool
	// Output is where the strip is routed: 0 is Stereo Out, n is Bus n, -1
	// is no output and -2 is Surround. Save can route a strip between Stereo Out and the buses.
	Output int16
	// Input is an aux's input bus, or -1 for none. Save can move an aux from
	// one bus to another.
	Input int16
	// Color is the track's color code, which counts through Logic's palette
	// from its 74th entry; see [Track.PaletteColor]. It is stored in the
	// strip's environment object, and is zero when the strip has none.
	Color uint8
	Sends []Send
	// Automation is the track's automation points, in time order, as placed
	// in Logic; see [AutomationPoint.Save].
	Automation []AutomationPoint
	// RecordArm, Protected, Hidden and Off are the track header's buttons,
	// stored in the track's arrange track; they are false when it has none.
	// RecordArm is read only: on opening a project Logic arms the selected
	// track instead, even in a project it saved armed.
	RecordArm  bool
	Protected  bool
	Hidden     bool
	Off        bool
	Instrument *AudioUnit
	MIDIFX     []AudioUnit
	AudioFX    []AudioUnit
	strip      uint16
	chunk      *Chunk
	// trak is the arrange track, which holds the header's buttons.
	trak *Chunk
	// environment is the strip's environment object, which holds its color.
	environment *Chunk
	// mixer reports whether the strip is long enough to hold the mixer
	// settings.
	mixer bool
	// name, color, output and input are as decoded, so that Save writes
	// them only when they were changed.
	name          string
	color         uint8
	output, input int16
	routes        *routes
}

// Mixer settings within a channel strip chunk. The volume is stored twice:
// as 8.24 fixed point, and as its whole part alone.
const (
	channelStripMonitor  = 84 // 0x08 is input monitoring
	channelStripVolume7  = 85
	channelStripSolo     = 88 // 0x01 is solo
	channelStripPan      = 89 // 64 is center
	channelStripMute     = 90 // 0x01 is mute; 0x02 is muted by another's solo
	channelStripOutput   = 92
	channelStripInput    = 94
	channelStripVolume   = 116
	channelStripMixerEnd = channelStripVolume + 4
)

// An arrange track is a "Trak" chunk in group 23, sequence 4, that names its
// strip's environment object.
const (
	arrangeTrackGroup       = 23
	arrangeTrackSequence    = 4
	arrangeTrackFlags       = 2 // 0x01 record arm, 0x20 protected
	arrangeTrackState       = 3 // 0x04 hidden, 0x20 off
	arrangeTrackEnvironment = 8
	arrangeTrackMinimum     = arrangeTrackEnvironment + 4
)

// headerFields is the layout of the track header's buttons in its arrange
// track that Save writes; RecordArm is decoded besides.
func (t *Track) headerFields() []record.Field {
	return []record.Field{
		record.Bit(arrangeTrackFlags, 0x20, &t.Protected),
		record.Bit(arrangeTrackState, 0x04, &t.Hidden),
		record.Bit(arrangeTrackState, 0x20, &t.Off),
	}
}

// findArrangeTracks maps environment object sequence numbers to the arrange
// track naming each. Logic can give a strip several, which is rare; the
// first is kept.
func findArrangeTracks(chunks []*Chunk) map[uint32]*Chunk {
	traks := make(map[uint32]*Chunk)
	for _, chunk := range chunks {
		if chunk.Type != "Trak" || len(chunk.Data) < arrangeTrackMinimum ||
			chunkSequenceID(chunk) != (chordSequenceID{arrangeTrackGroup, arrangeTrackSequence}) {
			continue
		}
		object := binary.LittleEndian.Uint32(chunk.Data[arrangeTrackEnvironment:])
		if _, ok := traks[object]; !ok {
			traks[object] = chunk
		}
	}
	return traks
}

// mixerFields is the layout of a strip's mixer settings; volume7 and pan
// hold the stored forms of the volume's whole part and of Pan.
func (t *Track) mixerFields(volume7, pan *uint8) []record.Field {
	return []record.Field{
		record.Bit(channelStripMonitor, 0x08, &t.InputMonitoring),
		record.Uint8(channelStripVolume7, volume7),
		record.Bit(channelStripSolo, 0x01, &t.Solo),
		record.Uint8(channelStripPan, pan),
		record.Bit(channelStripMute, 0x01, &t.Mute),
		record.Uint32LE(channelStripVolume, &t.Volume),
	}
}

// unityVolume is Track.Volume at 0 dB.
const unityVolume = 90 << 24

// VolumeDB returns the fader level in decibels, -Inf when it is all the way
// down. Logic's fader follows the square of its position: 40·log10(v/90).
func (t *Track) VolumeDB() float64 {
	return 40 * math.Log10(float64(t.Volume)/unityVolume)
}

// SetVolumeDB sets the fader level in decibels, limited to Logic's range.
func (t *Track) SetVolumeDB(db float64) { t.Volume = volumeFromDB(db) }

func volumeFromDB(db float64) uint32 {
	v := math.Round(unityVolume * math.Pow(10, db/40))
	return uint32(min(max(v, 0), 127<<24))
}

// Logic's color palette has 4 rows of 24 colors, which the color codes count
// through row by row, starting at the code paletteFirst in the top left.
const (
	paletteColumns = 24
	paletteSize    = 4 * paletteColumns
	paletteFirst   = 73
	// environmentColor is where an environment object holds its color.
	environmentColor = 155
)

// PaletteColor returns the row and column of t's color in Logic's color
// palette, counted from zero at the top left.
func (t *Track) PaletteColor() (row, column int) {
	i := (int(t.Color) + paletteSize - paletteFirst) % paletteSize
	return i / paletteColumns, i % paletteColumns
}

// SetPaletteColor sets t's color to the one at row and column of Logic's
// color palette, counted from zero at the top left.
func (t *Track) SetPaletteColor(row, column int) {
	t.Color = uint8((row*paletteColumns + column + paletteFirst) % paletteSize)
}

// Send is one of a channel strip's sends, a plug-in chunk of its own.
type Send struct {
	// Index is the send's slot on the strip, from zero.
	Index uint8
	// Bus is the bus it sends to.
	Bus uint8
	// Level is the send level on the volume fader's scale; see
	// [Track.Volume]. Zero is -Inf dB, where Logic starts a new send.
	Level uint32
	// Pan runs from -64, fully left, to 63, fully right.
	Pan      int8
	PreFader bool
	chunk    *Chunk
	bus      uint8
	routes   *routes
}

// Within a send's chunk. As with the fader, the level is stored twice.
const (
	sendIndex   = 6
	sendPost    = 16
	sendLevel7  = 17
	sendPre     = 18
	sendBus     = 20 // the bus number plus one
	sendLevel   = 24
	sendPan     = 28 // 64 is center
	sendMinimum = sendPan + 1
	// sendDestination holds the ID of the bus, after the send's own.
	sendDestination = 60
	chainSend       = 0
)

// fields is the layout of a send; level7, pan and post hold the stored forms
// of Level's whole part, of Pan, and of the post-fader flag.
func (s *Send) fields(level7, pan *uint8, post *bool) []record.Field {
	return []record.Field{
		record.Bit(sendPost, 0x01, post),
		record.Uint8(sendLevel7, level7),
		record.Bit(sendPre, 0x01, &s.PreFader),
		record.Uint8(sendPan, pan),
		record.Uint32LE(sendLevel, &s.Level),
	}
}

// findSends decodes the sends in chunks by the strip they belong to.
func findSends(chunks []*Chunk, routes *routes) map[uint16][]Send {
	sends := make(map[uint16][]Send)
	for _, chunk := range chunks {
		if chunk.Type != pluginChunk || !bytes.Equal(chunk.Header[4:8], pluginVariant) ||
			len(chunk.Data) < sendMinimum || binary.LittleEndian.Uint16(chunk.Data[pluginChainOffset:]) != chainSend {
			continue
		}
		s := Send{Index: chunk.Data[sendIndex], Bus: chunk.Data[sendBus] - 1, chunk: chunk, routes: routes}
		s.bus = s.Bus
		var level7, pan uint8
		var post bool
		record.Decode(chunk.Data, s.fields(&level7, &pan, &post)...)
		s.Pan = int8(pan - 64)
		strip := binary.LittleEndian.Uint16(chunk.Header[14:])
		sends[strip] = append(sends[strip], s)
	}
	return sends
}

// Save writes s's level, pan and pre-fader setting into the chunk it was
// decoded from. ProjectData.Tracks is not updated; see [ProjectData.Refresh].
func (s *Send) Save() error {
	if s.chunk == nil {
		return errors.New("logicx: send was not decoded from a project")
	}
	if s.Level > 127<<24 || s.Pan < -64 {
		return fmt.Errorf("logicx: send level %#x or pan %d out of range", s.Level, s.Pan)
	}
	level7, pan, post := uint8(s.Level>>24), uint8(s.Pan+64), !s.PreFader
	data, err := record.Encode(s.chunk.Data, s.fields(&level7, &pan, &post)...)
	if err != nil {
		return fmt.Errorf("logicx: send %d: %w", s.Index, err)
	}
	if s.Bus != s.bus {
		// The bus's ID follows the send's own; only that one is replaced.
		if len(data) < sendDestination+routeIDSize {
			return fmt.Errorf("logicx: send %d has no destination to re-route", s.Index)
		}
		end := sendDestination + routeIDSize
		head, err := s.routes.reroute(data[sendDestination:end], int16(s.bus), int16(s.Bus))
		if err != nil {
			return fmt.Errorf("logicx: send %d: %w", s.Index, err)
		}
		copy(data[sendDestination:], head)
		data[sendBus] = s.Bus + 1
	}
	s.chunk.Data, s.bus = data, s.Bus
	return nil
}

// AutomationPoint is one point of a track's automation.
type AutomationPoint struct {
	// Position and Fraction are on the same timeline as [MIDINote.Position].
	Position uint32
	Fraction uint16
	// Slot is what the point automates: 0 is the channel strip, 1 its
	// instrument and 1+n its audio effect in slot n (see [AudioUnit.Slot]),
	// or with MIDIFX, 1+n is its MIDI effect in slot n.
	Slot   uint8
	MIDIFX bool
	// Parameter is, on the channel strip, an [AutomationParameter], and
	// otherwise the plug-in's parameter number.
	Parameter AutomationParameter
	// Value is 8.24 fixed point: the volume on the fader's scale (see
	// [Track.Volume]), the pan from 0, fully left, to 127, the mute as 0 or
	// 1, and a plug-in parameter over its range scaled to 0 to 127.
	Value uint32
	// Curve bends the ramp from this point to the next, from -126 to 126 in
	// steps of two; zero is straight. Over a fraction f of the way, the value
	// covers f / (f + r·(1-f)) of the change, where r is 1 + Curve/10 for a
	// positive Curve, which holds near this point's value longer, and the
	// reciprocal of 1 + |Curve|/10 for a negative one. An SCurve is two such
	// halves mirrored about the middle, with r = 1 + |Curve|/5.
	Curve  int8
	SCurve bool
	ref    eventRef
}

// A point with a curve carries a second atom with the curve's byte. Its
// lowest bit marks an S-curve.
const (
	automationCurveAtom = 0xbb
	automationCurve     = atomSize + 6
)

// fields is the layout of an automation point; parameter holds the stored
// form of Parameter.
func (a *AutomationPoint) fields(eventType, parameter *uint8) []record.Field {
	return []record.Field{
		record.Uint8(0, eventType),
		record.Uint16LE(2, &a.Fraction),
		record.Uint32LE(4, &a.Position),
		record.Uint32LE(8, &a.Value),
		record.Uint8(12, parameter),
	}
}

// Save writes a back into its automation sequence, keeping the sequence in
// time order, and adds or removes its curve atom as Curve requires. Logic also stores samples of the ramps between points, which
// it does not need: it draws and saves the automation from the points alone.
// Save drops the samples of a's parameter rather than leave them stale.
// Track.Automation is not updated; see [ProjectData.Refresh].
func (a *AutomationPoint) Save() error {
	if a.Value > 127<<24 {
		return fmt.Errorf("logicx: automation value %#x out of range", a.Value)
	}
	if a.Slot > maxAutomationSlot {
		return fmt.Errorf("logicx: automation slot %d out of range", a.Slot)
	}
	if a.Curve&1 != 0 || a.Curve == math.MinInt8 {
		return fmt.Errorf("logicx: automation curve %d is not an even number from -126 to 126", a.Curve)
	}
	if err := a.ref.check("automation point"); err != nil {
		return err
	}
	a.ref.event.Data = a.withCurve(a.ref.event.Data)
	// Samples of the slot and parameter the point had are dropped too, as
	// its ramps there are gone.
	old := a.ref.event.Data
	eventType, parameter := a.eventType(), uint8(a.Parameter)
	oldType, oldParameter := old[0], old[12]
	if err := a.ref.save("automation point", a.fields(&eventType, &parameter)...); err != nil {
		return err
	}
	a.ref.dropSamples(eventType, parameter)
	a.ref.dropSamples(oldType, oldParameter)
	return nil
}

// Delete removes a from its automation sequence, along with the samples of
// its parameter; see [AutomationPoint.Save].
func (a *AutomationPoint) Delete() error {
	if err := a.ref.delete("automation point"); err != nil {
		return err
	}
	a.ref.dropSamples(a.eventType(), uint8(a.Parameter))
	return nil
}

// Duplicate inserts a copy of a right after it and returns the copy, which
// is saved to place it; see [AutomationPoint.Save].
func (a *AutomationPoint) Duplicate() (AutomationPoint, error) {
	ref, err := a.ref.duplicate("automation point")
	copied := *a
	copied.ref = ref
	return copied, err
}

// eventType is the event type of a's slot.
func (a *AutomationPoint) eventType() uint8 {
	if a.MIDIFX {
		return eventMIDIFXAutomation + a.Slot
	}
	return eventAutomation + a.Slot
}

// withCurve returns the point's record with its curve atom set from Curve
// and SCurve, added after the first atom or removed when the ramp is
// straight.
func (a *AutomationPoint) withCurve(data []byte) []byte {
	has := len(data) >= 2*atomSize && data[atomSize+7] == automationCurveAtom
	code := uint8(a.Curve)
	if a.SCurve {
		code |= 1
	}
	switch {
	case code == 0 && has:
		return slices.Concat(data[:atomSize], data[2*atomSize:])
	case code == 0:
		return data
	case !has:
		atom := make([]byte, atomSize)
		atom[7] = automationCurveAtom
		data = slices.Concat(data[:atomSize], atom, data[atomSize:])
	default:
		data = slices.Clone(data)
	}
	data[automationCurve] = code
	return data
}

// dropSamples removes the ramp samples of a slot's parameter, given by its
// event type, from the sequence.
func (r eventRef) dropSamples(eventType, parameter uint8) {
	r.chunk.Events = slices.DeleteFunc(r.chunk.Events, func(e *Event) bool {
		return e.Data[0] == eventType && len(e.Data) >= atomSize && e.Data[12] == parameter && e.Data[15]&automationSample != 0
	})
}

// AutomationParameter is what an automation point controls, numbered like
// the MIDI controllers.
type AutomationParameter uint8

const (
	AutomationVolume     AutomationParameter = 7
	AutomationMute       AutomationParameter = 9
	AutomationPan        AutomationParameter = 10
	AutomationSend1Level AutomationParameter = 28
)

const (
	// eventAutomation is the event type of channel strip automation; each
	// slot's automation takes the type that many after it, and MIDI
	// effects' automation counts likewise from eventMIDIFXAutomation.
	eventAutomation       = 0x50
	eventMIDIFXAutomation = 0x40
	maxAutomationSlot     = 0x0f
	// automationSample marks the events Logic adds between two points to
	// sample the ramp, or curve, joining them.
	automationSample = 0x40
	// automationTrack is where an automation sequence's descriptor names its
	// track's environment object, within the descriptor's tail.
	automationTrack    = 204
	automationSequence = "*Automation"
)

// findAutomation decodes the automation points of each automation sequence,
// by the environment object of the track it belongs to.
func findAutomation(chunks []*Chunk) map[uint32][]AutomationPoint {
	tracks := make(map[chordSequenceID]uint32)
	for _, chunk := range chunks {
		if chunk.Type != "MSeq" || sequenceName(chunk.Data) != automationSequence {
			continue
		}
		if tail, ok := sequenceTail(chunk.Data); ok && tail+automationTrack+4 <= len(chunk.Data) {
			tracks[chunkSequenceID(chunk)] = binary.LittleEndian.Uint32(chunk.Data[tail+automationTrack:])
		}
	}
	points := make(map[uint32][]AutomationPoint)
	for _, chunk := range chunks {
		track, ok := tracks[chunkSequenceID(chunk)]
		if chunk.Type != "EvSq" || !ok {
			continue
		}
		for _, event := range chunk.Events {
			d := event.Data
			base := d[0] &^ maxAutomationSlot
			if base != eventAutomation && base != eventMIDIFXAutomation || d[15]&automationSample != 0 {
				continue
			}
			points[track] = append(points[track], AutomationPoint{
				Position: binary.LittleEndian.Uint32(d[4:]), Fraction: binary.LittleEndian.Uint16(d[2:]),
				Slot: d[0] - base, MIDIFX: base == eventMIDIFXAutomation,
				Parameter: AutomationParameter(d[12]), Value: binary.LittleEndian.Uint32(d[8:]),
				ref: eventRef{chunk, event},
			})
			if len(d) >= 2*atomSize && d[atomSize+7] == automationCurveAtom {
				point := &points[track][len(points[track])-1]
				point.Curve, point.SCurve = int8(d[automationCurve]&^1), d[automationCurve]&1 != 0
			}
		}
	}
	return points
}

// environmentStrip returns the channel strip an environment object names,
// stored plus one after its name, at the next even offset.
func environmentStrip(data []byte) (uint16, bool) {
	var name string
	if !record.Decode(data, record.String16(environmentNameLength, &name)) {
		return 0, false
	}
	at := environmentNameLength + 2 + len(name)
	at += at & 1
	if at+2 > len(data) {
		return 0, false
	}
	strip := binary.LittleEndian.Uint16(data[at:])
	return strip - 1, strip != 0
}

// Strips, and sends, name what they route to by ID. A strip in use has an ID
// of its own; any other has one made from its kind and index. A strip's
// chunk ends with its own ID, its output's and its input's.
const routeIDSize = 16

// routes holds the IDs of the strips a route can name, by route: 0 for
// Stereo Out and n for Bus n.
type routes struct{ ids map[int16][]byte }

// syntheticRouteID is the ID of a strip not in use.
func syntheticRouteID(kind byte, index uint16) []byte {
	id := []byte{0xee, 0, 0, 0, 0, 0, 0x80, 0, 0x80, kind & 0x0f, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(id[11:], index)
	return id
}

// stereoOutKind is the kind byte of a stereo output strip; Stereo Out is the
// first.
const stereoOutKind = 0x4c

// findRoutes reads the IDs of Stereo Out and of the buses. An ID is taken
// only when it is the one expected of a strip not in use, or another strip
// names it, so that a strip of some other layout is not misread.
func findRoutes(chunks []*Chunk) *routes {
	r := &routes{ids: make(map[int16][]byte)}
	var strips []*Chunk
	for _, chunk := range chunks {
		if isChannelStrip(chunk) && len(chunk.Data) >= channelStripRecord+3*routeIDSize {
			strips = append(strips, chunk)
		}
	}
	named := func(id []byte, self *Chunk) bool {
		for _, other := range strips {
			if other != self && bytes.Contains(other.Data, id) {
				return true
			}
		}
		return false
	}
	for _, chunk := range strips {
		d := chunk.Data
		kind, index := d[channelStripKind], binary.LittleEndian.Uint16(d[channelStripKind+2:])
		id := d[len(d)-3*routeIDSize : len(d)-2*routeIDSize]
		if !bytes.Equal(id, syntheticRouteID(kind, index)) && (allZero(id) || !named(id, chunk)) {
			continue
		}
		switch {
		case kind == stereoOutKind && index == 0:
			r.ids[0] = id
		case trackKind(d) == TrackKindBus:
			r.ids[int16(index)+1] = id
		}
	}
	return r
}

// reroute returns data with the ID of route from replaced by that of route
// to. It refuses unless both are known and from's ID occurs in data exactly
// once, so that nothing else is overwritten.
func (r *routes) reroute(data []byte, from, to int16) ([]byte, error) {
	old, ok := r.ids[from]
	if !ok {
		return nil, fmt.Errorf("route %d is not one this package can change", from)
	}
	id, ok := r.ids[to]
	if !ok {
		return nil, fmt.Errorf("route %d is not one this package can name", to)
	}
	at := bytes.Index(data, old)
	if at < 0 || bytes.Count(data, old) != 1 {
		return nil, fmt.Errorf("route %d is not named exactly once", from)
	}
	out := bytes.Clone(data)
	copy(out[at:], id)
	return out, nil
}

// stripEnvironment finds the environment object of a channel strip: the
// strip holds the ID that ends each object, its own first and then those of
// the objects it routes to.
func stripEnvironment(strip []byte, objects []*Chunk) *Chunk {
	var own *Chunk
	first := len(strip)
	for _, object := range objects {
		if len(object.Data) <= environmentColor+16 {
			continue
		}
		id := object.Data[len(object.Data)-16:]
		if allZero(id) {
			continue
		}
		if at := bytes.Index(strip, id); at >= 0 && at < first {
			own, first = object, at
		}
	}
	return own
}

// Plug-in instances are stored one per chunk, sharing a chunk type with other
// audio configuration data. The header variant selects them and the header's
// trailing field names the channel strip they belong to, so no plug-in has to
// be located by searching or matched to a strip by proximity.
const (
	pluginChunk        = "AuCU"
	pluginChainOffset  = 4
	pluginSlotOffset   = 6
	pluginSetting      = 14
	pluginSettingSize  = 64
	pluginName         = 120
	pluginNameSize     = 12
	pluginManufacturer = 132
	pluginType         = 136
	pluginSubtype      = 140
	pluginRecordSize   = 144
)

// pluginVariant marks an AuCU chunk as a plug-in instance.
var pluginVariant = []byte{0x05, 0x00, 0x0e, 0x00}

// emagic is the manufacturer code on every plug-in that ships with Logic.
const emagic = "EMAG"

// Chains within a channel strip. Logic keeps the instrument and the audio
// inserts in one chain and the MIDI effects in another. The remaining chains
// hold the strip's setting name and its interface state rather than plug-ins.
const (
	chainAudio  = 1
	chainMIDIFX = 2
)

// findAudioUnits decodes one plug-in instance per chunk, in file order.
func findAudioUnits(chunks []*Chunk) []AudioUnit {
	var found []AudioUnit
	for _, chunk := range chunks {
		if chunk.Type != pluginChunk || !bytes.Equal(chunk.Header[4:8], pluginVariant) ||
			len(chunk.Data) < pluginRecordSize {
			continue
		}
		chain := binary.LittleEndian.Uint16(chunk.Data[pluginChainOffset:])
		if chain != chainAudio && chain != chainMIDIFX {
			continue
		}
		unit := AudioUnit{
			Name:         cString(chunk.Data[pluginName : pluginName+pluginNameSize]),
			Type:         componentCode(chunk.Data[pluginType:pluginSubtype]),
			Subtype:      componentCode(chunk.Data[pluginSubtype:pluginRecordSize]),
			Manufacturer: componentCode(chunk.Data[pluginManufacturer:pluginType]),
			Setting:      cString(chunk.Data[pluginSetting : pluginSetting+pluginSettingSize]),
			Slot:         binary.LittleEndian.Uint16(chunk.Data[pluginSlotOffset:]),
			Offset:       chunk.Offset,
			strip:        binary.LittleEndian.Uint16(chunk.Header[14:]),
			chunk:        chunk,
		}
		unit.name, unit.setting = unit.Name, unit.Setting
		if unit.Name == "" && unit.Manufacturer == "" {
			continue
		}
		unit.midi = chain == chainMIDIFX
		found = append(found, unit)
	}
	return found
}

// cString reads a NUL-padded name, rejecting anything not printable.
func cString(b []byte) string {
	if zero := bytes.IndexByte(b, 0); zero >= 0 {
		b = b[:zero]
	}
	if !printable(b) {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// componentCode reads a four-character code, which is empty when unset.
func componentCode(b []byte) string {
	if allZero(b) {
		return ""
	}
	if !printable4(b) {
		return ""
	}
	return reverse4(b)
}

// Channel strips are stored one per chunk. The chunk type is shared with other
// audio configuration data, so the header variant selects strips, and the
// record then sits at a fixed offset rather than needing to be searched for.
const (
	channelStripChunk       = "AuCO"
	channelStripRecord      = 60
	channelStripRecordSize  = 24
	channelStripHeaderStart = 4
)

// channelStripVariant is the header field that marks an AuCO chunk as a
// channel strip rather than another kind of audio configuration. Its first
// byte is a version, 5 to 7 depending on the Logic that wrote the project,
// with the same record layout in each.
var channelStripVariant = []byte{0x07, 0x00, 0x0e, 0x00}

// isChannelStrip reports whether chunk is a channel strip.
func isChannelStrip(chunk *Chunk) bool {
	v := chunk.Header[channelStripHeaderStart : channelStripHeaderStart+4]
	return chunk.Type == channelStripChunk && v[0] >= 5 && v[0] <= channelStripVariant[0] && bytes.Equal(v[1:], channelStripVariant[1:])
}

// findTracks decodes one channel strip per chunk. The record is a leading
// byte, a name padded to 16 bytes, and an 8-byte descriptor. Chunks are in
// file order, so the result is ordered by offset, which assignAudioUnits
// relies on.
func findTracks(chunks []*Chunk) []Track {
	var tracks []Track
	routes := findRoutes(chunks)
	sends := findSends(chunks, routes)
	automation := findAutomation(chunks)
	traks := findArrangeTracks(chunks)
	var objects []*Chunk
	byStrip := make(map[uint16][]*Chunk)
	for _, chunk := range chunks {
		if chunk.Type == environmentChunk {
			objects = append(objects, chunk)
			if strip, ok := environmentStrip(chunk.Data); ok {
				byStrip[strip] = append(byStrip[strip], chunk)
			}
		}
	}
	for _, chunk := range chunks {
		if !isChannelStrip(chunk) || len(chunk.Data) < channelStripRecord+channelStripRecordSize {
			continue
		}
		strip := chunk.Data[channelStripRecord : channelStripRecord+channelStripRecordSize]
		field := strip[:16]
		nameEnd := 16
		if zero := bytes.IndexByte(field[1:], 0); zero >= 0 {
			nameEnd = zero + 1
		}
		if nameEnd == 1 || !allZero(field[nameEnd:]) || !printable(field[1:nameEnd]) {
			continue
		}
		name := strings.TrimSpace(string(field[1:nameEnd]))
		if name == "" {
			continue
		}
		descriptor := strip[16:24]
		track := Track{
			Name: name, Kind: trackKind(chunk.Data),
			Offset: chunk.Offset + chunkHeaderSize + channelStripRecord,
			Active: descriptor[2]&0x04 != 0 || descriptor[4] != 0,
			strip:  binary.LittleEndian.Uint16(chunk.Header[14:]),
			chunk:  chunk, name: name,
		}
		if len(chunk.Data) >= channelStripMixerEnd {
			var volume7, pan uint8
			record.Decode(chunk.Data, track.mixerFields(&volume7, &pan)...)
			track.Pan, track.mixer = int8(pan-64), true
			track.Output = int16(binary.LittleEndian.Uint16(chunk.Data[channelStripOutput:]))
			track.Input = int16(binary.LittleEndian.Uint16(chunk.Data[channelStripInput:]))
			track.output, track.input, track.routes = track.Output, track.Input, routes
		}
		track.Sends = sends[track.strip]
		slices.SortFunc(track.Sends, func(a, b Send) int { return cmp.Compare(a.Index, b.Index) })
		// Older projects have no IDs to find the strip's own object by; then
		// it is the only object naming the strip.
		track.environment = stripEnvironment(chunk.Data, objects)
		if named := byStrip[track.strip]; track.environment == nil && len(named) == 1 {
			track.environment = named[0]
		}
		if track.environment != nil {
			track.Color = track.environment.Data[environmentColor]
			track.color = track.Color
			if track.trak = traks[chunkSequenceID(track.environment).sequence]; track.trak != nil {
				record.Decode(track.trak.Data, append(track.headerFields(), record.Bit(arrangeTrackFlags, 0x01, &track.RecordArm))...)
			}
		}
		// Automation may belong to any of the strip's objects, such as the
		// track's own object besides the strip's.
		for _, object := range byStrip[track.strip] {
			track.Automation = append(track.Automation, automation[chunkSequenceID(object).sequence]...)
		}
		if track.environment != nil && !slices.Contains(byStrip[track.strip], track.environment) {
			track.Automation = append(track.Automation, automation[chunkSequenceID(track.environment).sequence]...)
		}
		slices.SortStableFunc(track.Automation, func(a, b AutomationPoint) int {
			return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.Fraction, b.Fraction))
		})
		tracks = append(tracks, track)
	}
	return tracks
}

// channelStripName is where the name sits within a channel strip chunk,
// after the record's leading byte.
const (
	channelStripName     = channelStripRecord + 1
	channelStripNameSize = 15
)

// Save writes t's mixer settings, and its name when it was changed, into the
// channel strip it was decoded from, and its color into the strip's
// environment object and its header's buttons, but for RecordArm, into its
// arrange track, and
// re-routes it when Output or Input was changed.
// Kind and Active are not written, nor are the sends; see [Send.Save]. ProjectData.Tracks is not updated; see
// [ProjectData.Refresh].
func (t *Track) Save() error {
	if t.chunk == nil {
		return errors.New("logicx: track was not decoded from a project")
	}
	var fields []record.Field
	if t.Name != t.name {
		if t.Name == "" || !printable([]byte(t.Name)) {
			return fmt.Errorf("logicx: track name %q is not non-empty printable ASCII", t.Name)
		}
		fields = append(fields, record.CString(channelStripName, channelStripNameSize, &t.Name))
	}
	if t.mixer {
		if t.Volume > 127<<24 || t.Pan < -64 {
			return fmt.Errorf("logicx: track %q: volume %#x or pan %d out of range", t.Name, t.Volume, t.Pan)
		}
		volume7, pan := uint8(t.Volume>>24), uint8(t.Pan+64)
		fields = append(fields, t.mixerFields(&volume7, &pan)...)
	}
	data, err := record.Encode(t.chunk.Data, fields...)
	if err != nil {
		return fmt.Errorf("logicx: track %q: %w", t.Name, err)
	}
	for _, route := range []struct {
		value, decoded int16
		offset         int
	}{{t.Output, t.output, channelStripOutput}, {t.Input, t.input, channelStripInput}} {
		if route.value == route.decoded {
			continue
		}
		if !t.mixer {
			return fmt.Errorf("logicx: track %q has no route to change", t.Name)
		}
		if data, err = t.routes.reroute(data, route.decoded, route.value); err != nil {
			return fmt.Errorf("logicx: track %q: %w", t.Name, err)
		}
		binary.LittleEndian.PutUint16(data[route.offset:], uint16(route.value))
	}
	// Older projects hold codes past the palette, so only a changed color
	// is checked.
	if t.environment != nil && t.Color != t.color {
		if t.Color >= paletteSize {
			return fmt.Errorf("logicx: track %q: color %d out of range", t.Name, t.Color)
		}
		t.environment.Data[environmentColor] = t.Color
	}
	if t.trak != nil {
		trak, err := record.Encode(t.trak.Data, t.headerFields()...)
		if err != nil {
			return fmt.Errorf("logicx: track %q: %w", t.Name, err)
		}
		t.trak.Data = trak
	}
	t.chunk.Data, t.name, t.color = data, t.Name, t.Color
	t.output, t.input = t.Output, t.Input
	return nil
}

// Save writes a's slot, and its name and setting when they were changed, into
// the plug-in chunk it was decoded from. The component description is not
// written: it identifies the plug-in whose state the chunk holds.
// ProjectData.AudioUnits and the tracks are not updated; see
// [ProjectData.Refresh].
func (a *AudioUnit) Save() error {
	if a.chunk == nil {
		return errors.New("logicx: plug-in was not decoded from a project")
	}
	fields := []record.Field{record.Uint16LE(pluginSlotOffset, &a.Slot)}
	for _, f := range []struct {
		value, decoded *string
		offset, size   int
	}{{&a.Name, &a.name, pluginName, pluginNameSize}, {&a.Setting, &a.setting, pluginSetting, pluginSettingSize}} {
		if *f.value == *f.decoded {
			continue
		}
		if !printable([]byte(*f.value)) {
			return fmt.Errorf("logicx: plug-in text %q is not printable ASCII", *f.value)
		}
		fields = append(fields, record.CString(f.offset, f.size, f.value))
	}
	data, err := record.Encode(a.chunk.Data, fields...)
	if err != nil {
		return fmt.Errorf("logicx: plug-in %q: %w", a.Name, err)
	}
	a.chunk.Data, a.name, a.setting = data, a.Name, a.Setting
	return nil
}

// assignAudioUnits attaches each plug-in to the channel strip named in its
// chunk header. Within a strip the instrument occupies slot zero of the audio
// chain and the inserts follow it, so a chain with gaps keeps its numbering.
func assignAudioUnits(tracks []Track, units []AudioUnit) {
	byStrip := make(map[uint16][]AudioUnit, len(tracks))
	for _, unit := range units {
		byStrip[unit.strip] = append(byStrip[unit.strip], unit)
	}
	for i := range tracks {
		track := &tracks[i]
		strip := byStrip[track.strip]
		slices.SortFunc(strip, func(a, b AudioUnit) int { return cmp.Compare(a.Slot, b.Slot) })
		for _, unit := range strip {
			switch {
			case unit.midi:
				track.MIDIFX = append(track.MIDIFX, unit)
			case unit.Slot == 0 && track.Kind == TrackKindInstrument && track.Instrument == nil:
				u := unit
				track.Instrument = &u
			default:
				track.AudioFX = append(track.AudioFX, unit)
			}
		}
	}
}

// channelStripKind is the byte of a channel strip chunk whose low three bits
// say what kind of strip it is.
const channelStripKind = 4

// trackKinds are the kinds of channel strip, by their code.
var trackKinds = []TrackKind{
	TrackKindAudio, TrackKindInput, TrackKindAux, TrackKindInstrument,
	TrackKindOutput, TrackKindBus, TrackKindMaster,
}

// trackKind classifies a channel strip chunk's payload.
func trackKind(data []byte) TrackKind {
	if code := int(data[channelStripKind] & 0x07); code < len(trackKinds) {
		return trackKinds[code]
	}
	return TrackKindUnknown
}
