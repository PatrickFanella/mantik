package alpaca

// PublicationRecordings returns only verified canonical journal captures. It
// performs no acquisition, binding change, delivery, or filesystem mutation.
func (j *Journal) PublicationRecordings() ([]Recording, error) {
	captures, err := j.captures()
	if err != nil {
		return nil, err
	}
	out := make([]Recording, 0, len(captures))
	for _, capture := range captures {
		out = append(out, capture.Recording)
	}
	return out, nil
}
