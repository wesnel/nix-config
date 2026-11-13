package plans

import "time"

// DistributionPart is a Distribution entry, whose keyword follows its log.
type DistributionPart struct {
	ID, Title, Keyword string
}

// Distribution reads parts in file order, including those without a child file.
func (p Plan) Distribution() ([]DistributionPart, error) {
	items, err := p.itemList()
	if err != nil {
		return nil, err
	}
	events, err := p.Events()
	if err != nil {
		return nil, err
	}
	inDistribution, err := p.InSection("Distribution")
	if err != nil {
		return nil, err
	}
	var out []DistributionPart
	for _, it := range items {
		if partPattern.MatchString(it.ID) && inDistribution[it.ID] {
			out = append(out, DistributionPart{ID: it.ID, Title: it.Title, Keyword: stepState(events, it.ID)})
		}
	}
	return out, nil
}

// ItemTiming is an item's explicit dependencies and local scheduled time.
type ItemTiming struct {
	After            []string
	Scheduled        time.Time
	ScheduledHasTime bool
}

// ItemTiming reads timing metadata for the plan and all its parts.
func (p Plan) ItemTiming() (map[string]ItemTiming, error) {
	items, err := p.itemList()
	if err != nil {
		return nil, err
	}
	out := make(map[string]ItemTiming, len(items))
	for _, it := range items {
		out[it.ID] = ItemTiming{After: afterIDs(it.Props), Scheduled: it.Scheduled, ScheduledHasTime: it.ScheduledHasTime}
	}
	return out, nil
}
