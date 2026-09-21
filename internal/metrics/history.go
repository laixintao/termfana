package metrics

// History owns immutable snapshots. Definitions are shared across frames and
// reference counted, so labels from disappeared series are eventually released.
type History struct {
	frames       []Snapshot
	head, length int
	definitions  map[string]Definition
	refs         map[string]int
	maxSeries    int
	Trimmed      bool
}

func NewHistory(capacity, maxSeries int) *History {
	return &History{frames: make([]Snapshot, capacity), definitions: map[string]Definition{}, refs: map[string]int{}, maxSeries: maxSeries}
}

func (h *History) drop() {
	s := h.frames[h.head]
	for key := range s.Values {
		h.refs[key]--
		if h.refs[key] == 0 {
			delete(h.refs, key)
			delete(h.definitions, key)
		}
	}
	h.frames[h.head] = Snapshot{}
	h.head = (h.head + 1) % len(h.frames)
	h.length--
}

func (h *History) Append(s Snapshot) {
	h.Trimmed = false
	if h.length == len(h.frames) {
		h.drop()
	}
	for h.length > 0 {
		count := len(h.definitions)
		for k := range s.Values {
			if _, ok := h.definitions[k]; !ok {
				count++
			}
		}
		if count <= h.maxSeries {
			break
		}
		h.drop()
		h.Trimmed = true
	}
	for key := range s.Values {
		h.definitions[key] = s.Definitions[key]
		h.refs[key]++
	}
	s.Definitions = nil
	h.frames[(h.head+h.length)%len(h.frames)] = s
	h.length++
}

func (h *History) Snapshots() []Snapshot {
	frames := make([]Snapshot, h.length)
	for i := range frames {
		frames[i] = h.frames[(h.head+i)%len(h.frames)]
		frames[i].Definitions = h.definitions
	}
	return frames
}

func (h *History) Len() int { return h.length }
