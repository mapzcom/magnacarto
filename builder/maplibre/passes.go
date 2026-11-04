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

// labelProps describe a symbolizer whose passes are merged.
type labelProps struct {
	// view returns the text-* properties of a rule, e.g. the text of a
	// shield; nil for text itself.
	view func(p *mss.Properties) *mss.Properties
	// placement are the properties (keys of props) in which passes may
	// differ.
	placement map[string]bool
	// repeatDistance names the repeat distance property.
	repeatDistance string
}

// textView returns the text-* properties of a rule of a label symbolizer.
func (c symConverter) textView(p *mss.Properties) *mss.Properties {
	if c.label == nil || c.label.view == nil {
		return p
	}
	return c.label.view(p)
}

// labelPrefixes are the symbolizers whose passes are merged, in a fixed
// order.
var labelPrefixes = []string{"text-", "shield-"}

// mergePasses marks each run of passes per label symbolizer: the first
// group gets the passes, and each pass the Mapnik zoom levels at which its
// label is merged.
func (m *Map) mergePasses(layerID string, groups []attachmentGroup) []attachmentGroup {
	for _, prefix := range labelPrefixes {
		conv := converters[prefix]
		first := -1
		for i := range groups {
			if first >= 0 {
				if zooms := m.passZooms(conv, groups[first], groups[i]); len(zooms) > 0 {
					if groups[i].mergedZooms == nil {
						groups[i].mergedZooms = map[string]map[int]bool{}
					}
					if groups[first].passes == nil {
						groups[first].passes = map[string][]attachmentGroup{}
					}
					groups[i].mergedZooms[prefix] = zooms
					groups[first].passes[prefix] = append(groups[first].passes[prefix], groups[i])
					m.note(layerID, groups[i].name, prefix, Approximated,
						"merged into ::"+groups[first].name+" as further anchors: Mapnik places this pass only where the earlier ones failed ("+conv.label.repeatDistance+")")
					continue
				}
			}
			first = i
		}
	}
	return groups
}

// passZooms returns the Mapnik zoom levels at which the text of g repeats
// that of first for the same features, differing only in placement, and
// hides itself near it with text-repeat-distance. Rules of g with further
// conditions are allowed if they differ only in placement; their placement
// is not converted.
func (m *Map) passZooms(conv symConverter, first, g attachmentGroup) map[int]bool {
	zooms := map[int]bool{}
	for zm := 0; zm <= m.opts.MaxMapnikZoom; zm++ {
		if m.isPassAt(conv, first, g, zm) {
			zooms[zm] = true
		}
	}
	return zooms
}

func (m *Map) isPassAt(conv symConverter, first, g attachmentGroup, zm int) bool {
	a, b := validRules(first, zm), validRules(g, zm)
	drawn := false
	for _, ra := range a {
		pa := ra.rule.Properties
		pb, ok := ruleFor(b, ra.cond)
		if !ok {
			if conv.draws(pa) {
				return false
			}
			continue
		}
		da, db := conv.draws(pa), conv.draws(pb)
		if da != db {
			return false
		}
		if !da {
			continue
		}
		drawn = true
		if d, ok := pb.GetFloat(conv.label.repeatDistance); !ok || d <= 0 || !m.sameLabel(conv, pa, pb) {
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
			if ok && refines(rb.cond, rm.cond) && m.sameLabel(conv, pa, rb.rule.Properties) {
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

// sameLabel reports whether two rules draw the same label apart from its
// placement.
func (m *Map) sameLabel(conv symConverter, pa, pb *mss.Properties) bool {
	if !conv.draws(pa) || !conv.draws(pb) {
		return false
	}
	for name, pc := range conv.props {
		if conv.label.placement[name] {
			continue
		}
		va, oka := m.propValue(pc, pa)
		vb, okb := m.propValue(pc, pb)
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
// rules of the passes at the same position, from their text-* properties.
func mergedAnchors(p *mss.Properties, passes []*mss.Properties) interface{} {
	var all []interface{}
	for _, pp := range append([]*mss.Properties{p}, passes...) {
		all = append(all, anchorList(pp)...)
	}
	return literal(all)
}
