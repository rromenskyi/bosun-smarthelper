package agent

// repetitionDetector spots a model's output collapsing into an endless
// repeated short substring — seen live: a weak/confused model echoing a
// large tool result then degenerating into hundreds of repeats of a single
// padding-style token — so the stream can be cut off instead of relaying
// garbage to the user indefinitely.
type repetitionDetector struct {
	tail []rune
}

const (
	repetitionWindowRunes = 600
	repetitionMinRepeats  = 12
	repetitionMaxPeriod   = 40
)

// feed appends text and reports whether the trailing window now looks like
// a runaway repeated substring.
func (d *repetitionDetector) feed(text string) bool {
	d.tail = append(d.tail, []rune(text)...)
	if len(d.tail) > repetitionWindowRunes {
		d.tail = d.tail[len(d.tail)-repetitionWindowRunes:]
	}
	return hasRunawayRepetition(d.tail)
}

// hasRunawayRepetition reports whether tail ends with the same short
// substring (2-40 runes) repeated at least repetitionMinRepeats times in a
// row — deliberately narrow (that many exact consecutive repeats is
// essentially never legitimate prose, even repeated punctuation) so this
// never fires on ordinary text.
func hasRunawayRepetition(tail []rune) bool {
	n := len(tail)
	for period := 2; period <= repetitionMaxPeriod; period++ {
		span := period * repetitionMinRepeats
		if span > n {
			break
		}
		chunk := string(tail[n-period:])
		matched := true
		for i := 2; i <= repetitionMinRepeats; i++ {
			start := n - period*i
			if string(tail[start:start+period]) != chunk {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
