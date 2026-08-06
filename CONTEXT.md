# Logic Project

This context describes musical information recovered from a Logic project and
how that information relates to a reconstructed score.

## Language

**Region Chord**:
A chord owned by one MIDI region. It belongs on the same staff as that region.

**Project Chord**:
A project-wide chord independent of any MIDI region or track.

**Chord Group**:
Several project chords held by one child sequence rather than one each. The
group's link places its first chord and the rest keep their recorded spacing.

**Chord Staff**:
A score staff synthesized for project chords that are not already represented
by an existing staff.
_Avoid_: Chord track, project-chord track
