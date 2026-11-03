package maplibre

import (
	"github.com/omniscale/magnacarto/mss"
)

// Passes: CartoCSS often repeats a label in further attachments with other
// placements ("::center, ::var1, ::var2 { ... }"). Mapnik places a pass
// only where the earlier ones failed, because text-repeat-distance hides
// the same text near an already placed label. MapLibre has no repeat
// distance, so such passes are merged into the first attachment as further
// anchors of text-variable-anchor-offset, which MapLibre tries in order.

// placementProps are the text properties in which passes may differ.
var placementProps = map[string]bool{"text-placements": true, "text-dx": true, "text-justify-alignment": true}

// mergePasses marks each run of text passes: the first group gets the
// passes, and each pass the Mapnik zoom levels at which its text is merged.
func (m *Map) mergePasses(layerID string, groups []attachmentGroup) []attachmentGroup {
	first := -1
	for i := range groups {
		if first >= 0 {
			if zooms := m.passZooms(groups[first], groups[i]); len(zooms) > 0 {
				groups[i].mergedZooms = zooms
				groups[first].passes = append(groups[first].passes, groups[i])
				m.note(layerID, groups[i].name, "text-", Approximated,
					"merged into ::"+groups[first].name+" as further anchors: Mapnik places this pass only where the earlier ones failed (text-repeat-distance)")
				continue
			}
		}
		first = i
	}
	return groups
}

// passZooms returns the Mapnik zoom levels at which the text of g repeats
// that of first for the same features, differing only in placement, and
// hides itself near it with text-repeat-distance. Rules of g with further
// conditions are allowed if they differ only in placement; their placement
// is not converted.
func (m *Map) passZooms(first, g attachmentGroup) map[int]bool {
	zooms := map[int]bool{}
	for zm := 0; zm <= m.opts.MaxMapnikZoom; zm++ {
		if m.isPassAt(first, g, zm) {
			zooms[zm] = true
		}
	}
	return zooms
}

func (m *Map) isPassAt(first, g attachmentGroup, zm int) bool {
	a, b := validRules(first, zm), validRules(g, zm)
	drawn := false
	for _, ra := range a {
		pa := ra.rule.Properties
		pb, ok := ruleFor(b, ra.cond)
		if !ok {
			if textConverter.draws(pa) {
				return false
			}
			continue
		}
		da, db := textConverter.draws(pa), textConverter.draws(pb)
		if da != db {
			return false
		}
		if !da {
			continue
		}
		drawn = true
		if d, ok := pb.GetFloat("text-repeat-distance"); !ok || d <= 0 || !sameText(pa, pb) {
			return false
		}
	}
	// A further rule of g must refine a rule of g that first has too, e.g.
	// a placement exception for one name, and draw the same text.
	for _, rb := range b {
		if _, ok := ruleFor(a, rb.cond); ok {
			continue
		}
		found := false
		for _, rm := range b {
			pa, ok := ruleFor(a, rm.cond)
			if ok && refines(rb.cond, rm.cond) && sameText(pa, rb.rule.Properties) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return drawn
}

// refines reports whether condition c has all conjuncts of base.
func refines(c, base interface{}) bool {
	have := map[string]bool{}
	for _, x := range conjuncts(c) {
		have[string(mustJSON(x))] = true
	}
	for _, x := range conjuncts(base) {
		if !have[string(mustJSON(x))] {
			return false
		}
	}
	return true
}

func conjuncts(c interface{}) []interface{} {
	if c == true {
		return nil
	}
	if all, ok := c.([]interface{}); ok && len(all) > 0 && all[0] == "all" {
		return all[1:]
	}
	return []interface{}{c}
}

// sameText reports whether two rules draw the same text apart from its
// placement.
func sameText(pa, pb *mss.Properties) bool {
	if !textConverter.draws(pa) || !textConverter.draws(pb) {
		return false
	}
	for name, pc := range textConverter.props {
		if placementProps[name] {
			continue
		}
		va, oka := pc.value(pa)
		vb, okb := pc.value(pb)
		if oka != okb || !equal(va, vb) {
			return false
		}
	}
	return true
}

type validRule struct {
	rule mss.Rule
	cond interface{}
}

// validRules returns the rules of g for a Mapnik zoom level that can
// decide, in order: without rules whose condition an earlier one has.
func validRules(g attachmentGroup, zm int) []validRule {
	var result []validRule
	seen := map[string]bool{}
	for i, r := range g.rules {
		if !r.Zoom.ValidFor(zm) {
			continue
		}
		k := string(mustJSON(g.conds[i]))
		if !seen[k] {
			seen[k] = true
			result = append(result, validRule{r, g.conds[i]})
		}
	}
	return result
}

// ruleFor returns the properties of the rule with the condition.
func ruleFor(rules []validRule, cond interface{}) (*mss.Properties, bool) {
	for _, r := range rules {
		if equal(r.cond, cond) {
			return r.rule.Properties, true
		}
	}
	return nil, false
}

// mergedAnchors returns the anchors of a rule followed by those of the
// rules of the passes at the same position.
func mergedAnchors(p *mss.Properties, passes []*mss.Properties) interface{} {
	var all []interface{}
	for _, pp := range append([]*mss.Properties{p}, passes...) {
		all = append(all, anchorList(pp)...)
	}
	return literal(all)
}
