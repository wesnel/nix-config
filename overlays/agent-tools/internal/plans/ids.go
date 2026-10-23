package plans

import "os"

// IDs are the IDs of the plan's parts, steps, questions and follow-ups, in
// its own file and its parts'.
func (p Plan) IDs() ([]string, error) {
	files, err := p.files()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, h := range parseOrg(string(data)) {
			id := h.Props["ID"]
			if partPattern.MatchString(id) || stepPattern.MatchString(id) || questionID.MatchString(id) || followupID.MatchString(id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}
