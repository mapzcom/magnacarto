package maplibre

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/omniscale/magnacarto/color"
	"github.com/omniscale/magnacarto/mss"
)

// symbolizerPrefixes are all CartoCSS symbolizer prefixes, as in the Mapnik
// builder. Prefixes without a converter are reported as omitted.
var symbolizerPrefixes = []string{"line-", "line-pattern-", "polygon-", "polygon-pattern-", "text-", "shield-", "marker-", "point-", "building-", "raster-", "dot-"}

// propConv converts one CartoCSS property to one MapLibre property.
type propConv struct {
	target string
	layout bool
	// always properties are written even if no rule sets them, with def.
	always bool
	// zoomOnly properties take no data expressions in MapLibre; where the
	// value differs between features, the fallback value is used for all.
	zoomOnly bool
	// value returns the MapLibre value for a rule, or false if the rule does
	// not set the property.
	value func(p *mss.Properties) (interface{}, bool)
	// mapValue, if set, is used instead of value, for values that depend
	// on the Options.
	mapValue func(m *Map, p *mss.Properties) (interface{}, bool)
	// def is the MapLibre value for Mapnik's default, used for rules that
	// do not set the property while others do.
	def interface{}
}

type symConverter struct {
	layerType string
	// draws reports whether Mapnik creates the symbolizer for a rule.
	draws   func(p *mss.Properties) bool
	props   map[string]propConv
	omitted map[string]string // property -> reason, for the report
	// handled are properties converted by props of other names.
	handled []string
	// approximate returns the reason for the report if a property of a
	// rule is converted with a visible difference.
	approximate map[string]func(m *Map, p *mss.Properties) (string, bool)
	// constPaint is added to the paint of every layer the symbolizer draws.
	constPaint map[string]interface{}
	// constLayout is added to the layout of every layer the symbolizer draws.
	constLayout map[string]interface{}
	// approximated, if set, is reported for every rule that draws.
	approximated string
	// underlay, if set, converts an extra layer drawn below this one, with
	// underlaySuffix appended to the layer ID.
	underlay       *symConverter
	underlaySuffix string
	// blend, if set, names the properties Options.Blend works on.
	blend *blendProps
	// label, if set, merges passes of the symbolizer (see passes.go).
	label *labelProps
}

// blendProps are the compositing operation of a symbolizer and the colour
// and opacity properties that Options.Blend replaces.
type blendProps struct {
	compOp, color, opacity string
}

// blended returns the property values of a rule after Options.Blend, keyed
// by CartoCSS property, and the reason for the report; false if the rule has
// no compositing operation that Options.Blend approximates.
func (m *Map) blended(conv symConverter, p *mss.Properties) (map[string]interface{}, string, bool) {
	if conv.blend == nil || m.opts.Blend == nil {
		return nil, "", false
	}
	op, ok := p.GetString(conv.blend.compOp)
	if !ok {
		return nil, "", false
	}
	c, ok := p.GetColor(conv.blend.color)
	if !ok {
		c = color.MustParse("#000000")
	}
	opacity, ok := p.GetFloat(conv.blend.opacity)
	if !ok {
		opacity = 1
	}
	bc, bo, detail, ok := m.opts.Blend(op, c, opacity)
	if !ok {
		return nil, "", false
	}
	return map[string]interface{}{conv.blend.color: fmtColor(bc), conv.blend.opacity: bo}, detail, true
}

// buildingOpacity is shared by the walls and the roof of a building.
var buildingOpacity = propConv{target: "fill-opacity", value: floatValue("building-fill-opacity", 1), def: 1.0}

func drawsBuilding(p *mss.Properties) bool {
	_, ok := p.GetColor("building-fill")
	return ok
}

var converters = map[string]symConverter{
	"polygon-": {
		layerType: "fill",
		draws: func(p *mss.Properties) bool {
			_, ok := p.GetColor("polygon-fill")
			return ok
		},
		props: map[string]propConv{
			"polygon-fill":    {target: "fill-color", value: colorValue("polygon-fill")},
			"polygon-opacity": {target: "fill-opacity", value: floatValue("polygon-opacity", 1), def: 1.0},
		},
		omitted: map[string]string{
			"polygon-gamma":   "MapLibre antialiases fill edges itself (fill-antialias)",
			"polygon-comp-op": "MapLibre has no compositing operations",
		},
	},
	"polygon-pattern-": {
		layerType: "fill",
		draws: func(p *mss.Properties) bool {
			f, ok := p.GetString("polygon-pattern-file")
			return ok && f != ""
		},
		props: map[string]propConv{
			"polygon-pattern-file":    {target: "fill-pattern", value: imageValue("polygon-pattern-file")},
			"polygon-pattern-opacity": {target: "fill-opacity", value: floatValue("polygon-pattern-opacity", 1), def: 1.0},
		},
		omitted: map[string]string{
			"polygon-pattern-comp-op":   "MapLibre has no compositing operations",
			"polygon-pattern-gamma":     "MapLibre antialiases fill edges itself (fill-antialias)",
			"polygon-pattern-alignment": "MapLibre always aligns patterns to the world (Mapnik's global alignment)",
		},
	},
	"building-": {
		layerType: "fill",
		draws:     drawsBuilding,
		props: map[string]propConv{
			"building-fill":         {target: "fill-color", value: colorValue("building-fill")},
			"building-fill-opacity": buildingOpacity,
			"building-height":       {target: "fill-translate", zoomOnly: true, value: roofOffsetValue, def: literal([]float64{0, 0})},
		},
		// Mapnik draws the walls of a building in the fill colour times 0.8
		// and the roof on top, moved up by the height.
		underlaySuffix: "-walls",
		underlay: &symConverter{
			layerType: "fill",
			draws:     drawsBuilding,
			props: map[string]propConv{
				"building-fill":         {target: "fill-color", value: wallColorValue},
				"building-fill-opacity": buildingOpacity,
			},
		},
		// Mapnik moves the roof up on the screen, whatever the bearing.
		constPaint:   map[string]interface{}{"fill-translate-anchor": "viewport"},
		approximated: "flat: walls as a fill in building-fill × 0.8, the roof on top moved up by building-height (fill-translate); no 3D extrusion",
	},
	"line-": {
		layerType: "line",
		draws: func(p *mss.Properties) bool {
			w, ok := p.GetFloat("line-width")
			return ok && w != 0
		},
		props: map[string]propConv{
			"line-width":   {target: "line-width", value: floatValue("line-width", 1)},
			"line-color":   {target: "line-color", value: colorValue("line-color"), def: "#000000"},
			"line-opacity": {target: "line-opacity", value: floatValue("line-opacity", 1), def: 1.0},
			// Mapnik offsets to the left for positive values, MapLibre to the right.
			"line-offset":     {target: "line-offset", value: floatValue("line-offset", -1), def: 0.0},
			"line-dasharray":  {target: "line-dasharray", value: dashValue, def: literal([]float64{1, 0})},
			"line-cap":        {target: "line-cap", layout: true, value: stringValue("line-cap"), def: "butt"},
			"line-join":       {target: "line-join", layout: true, value: stringValue("line-join"), def: "miter"},
			"line-miterlimit": {target: "line-miter-limit", layout: true, value: floatValue("line-miterlimit", 1), def: 4.0},
		},
		omitted: map[string]string{
			"line-gamma":   "MapLibre antialiases lines itself",
			"line-comp-op": "MapLibre has no compositing operations",
		},
		blend: &blendProps{compOp: "line-comp-op", color: "line-color", opacity: "line-opacity"},
	},
	"text-":   textConverter,
	"shield-": shieldConverter,
	"point-":  pointConverter,
	"marker-": markerConverter,
	"raster-": {
		layerType: "raster",
		draws: func(p *mss.Properties) bool {
			o, ok := p.GetFloat("raster-opacity")
			return !ok || o != 0
		},
		props: map[string]propConv{
			"raster-opacity": {target: "raster-opacity", value: floatValue("raster-opacity", 1), def: 1.0},
			// Mapnik's default is "near".
			"raster-scaling": {target: "raster-resampling", value: scalingValue, def: "nearest"},
		},
		omitted: map[string]string{
			"raster-comp-op":                 "MapLibre has no compositing operations",
			"raster-mesh-size":               "MapLibre does not reproject rasters",
			"raster-colorizer-default-mode":  "MapLibre has no raster colorizer",
			"raster-colorizer-default-color": "MapLibre has no raster colorizer",
			"raster-colorizer-stops":         "MapLibre has no raster colorizer",
			"raster-colorizer-epsilon":       "MapLibre has no raster colorizer",
			"raster-filter-factor":           "MapLibre chooses the downscaling filter itself",
		},
	},
}

// scalingValue maps Mapnik's raster-scaling to MapLibre's raster-resampling,
// which knows only nearest and linear.
func scalingValue(p *mss.Properties) (interface{}, bool) {
	v, ok := p.GetString("raster-scaling")
	if !ok {
		return nil, false
	}
	if v == "near" {
		return "nearest", true
	}
	return "linear", true
}

// imageValue returns the sprite image name of a file: the path without its
// extension, e.g. "Pattern/wetland" for url(Pattern/wetland.svg).
func imageValue(name string) func(p *mss.Properties) (interface{}, bool) {
	return func(p *mss.Properties) (interface{}, bool) {
		f, ok := p.GetString(name)
		if !ok || f == "" {
			return nil, false
		}
		f = strings.TrimPrefix(path.Clean(f), "./")
		return strings.TrimSuffix(f, path.Ext(f)), true
	}
}

// wallColorValue returns building-fill times 0.8 per channel, truncated as
// in Mapnik's building symbolizer.
func wallColorValue(p *mss.Properties) (interface{}, bool) {
	c, ok := p.GetColor("building-fill")
	if !ok {
		return nil, false
	}
	r, g, b := c.ToRgb()
	wall := func(v float64) float64 { return math.Floor(math.Round(v*255)*0.8) / 255 }
	return fmtColor(color.FromRgba(wall(r), wall(g), wall(b), c.A, false)), true
}

// roofOffsetValue moves the roof up by building-height pixels.
func roofOffsetValue(p *mss.Properties) (interface{}, bool) {
	h, ok := p.GetFloat("building-height")
	if !ok {
		return nil, false
	}
	return literal([]float64{0, -h}), true
}

func colorValue(name string) func(p *mss.Properties) (interface{}, bool) {
	return func(p *mss.Properties) (interface{}, bool) {
		c, ok := p.GetColor(name)
		if !ok {
			return nil, false
		}
		return fmtColor(c), true
	}
}

func floatValue(name string, factor float64) func(p *mss.Properties) (interface{}, bool) {
	return func(p *mss.Properties) (interface{}, bool) {
		v, ok := p.GetFloat(name)
		return v * factor, ok
	}
}

func stringValue(name string) func(p *mss.Properties) (interface{}, bool) {
	return func(p *mss.Properties) (interface{}, bool) {
		v, ok := p.GetString(name)
		return v, ok
	}
}

// dashValue converts Mapnik's dash lengths in pixels to MapLibre's in line
// widths.
func dashValue(p *mss.Properties) (interface{}, bool) {
	dashes, ok := p.GetFloatList("line-dasharray")
	if !ok || len(dashes) == 0 {
		return nil, false
	}
	width, _ := p.GetFloat("line-width")
	if len(dashes)%2 == 1 {
		// SVG semantics, as in Mapnik: an odd list is repeated.
		dashes = append(dashes, dashes...)
	}
	pattern := make([]float64, len(dashes))
	var total float64
	for i, d := range dashes {
		pattern[i] = d / width
		total += pattern[i]
	}
	// MapLibre GL JS (6.15) draws nothing for a data-driven dasharray
	// shorter than one line width; the repeated pattern looks the same.
	result := pattern
	for sum := total; total > 0 && sum < 1; sum += total {
		result = append(result, pattern...)
	}
	return literal(result), true
}

func literal(v interface{}) interface{} {
	return []interface{}{"literal", v}
}

type convertedLayer struct {
	Layer
	first, last int // MapLibre zoom range, inclusive
	fields      []string
}

type caseEntry struct {
	cond  interface{}
	value interface{}
}

// convertSymbolizer converts one symbolizer of an attachment to a MapLibre
// layer. It returns false if the symbolizer draws nothing.
func (m *Map) convertSymbolizer(layerID string, g attachmentGroup, sym mss.Prefix, conv symConverter, suffix string) (convertedLayer, bool) {

	type zoomValues struct {
		zoom   int
		filter interface{}
		props  map[string]interface{} // nil if nothing is drawn
	}
	var zooms []zoomValues
	isSet := map[string]bool{}
	perFeature := map[string]bool{} // zoomOnly properties already reported
	first, last, lastMapnik := -1, -1, -1

	for zm := 0; zm <= m.opts.MaxMapnikZoom; zm++ {
		z := zm + m.opts.ZoomOffset
		if z < 0 {
			continue
		}
		var filterEntries []caseEntry
		propEntries := map[string][]caseEntry{}
		// A label merged into an earlier attachment is drawn there.
		merged := conv.label != nil && sym.Instance == ""
		if merged && g.mergedZooms[sym.Name][zm] {
			zooms = append(zooms, zoomValues{zoom: z, filter: false})
			continue
		}
		var passRules [][]validRule
		for _, pass := range g.passes[sym.Name] {
			if merged && pass.mergedZooms[sym.Name][zm] {
				passRules = append(passRules, validRules(pass, zm))
			}
		}
		for i, r := range g.rules {
			if !r.Zoom.ValidFor(zm) {
				continue
			}
			r.Properties.SetDefaultInstance(sym.Instance)
			cond := g.conds[i]
			draws := conv.draws(r.Properties)
			filterEntries = append(filterEntries, caseEntry{cond, draws})
			if draws {
				blended, _, _ := m.blended(conv, r.Properties)
				for name, pc := range conv.props {
					v, ok := m.propValue(pc, r.Properties)
					if bv, isBlended := blended[name]; isBlended {
						v, ok = bv, true
					}
					if pc.target == "text-variable-anchor-offset" && len(passRules) > 0 && !isLinePlacement(conv.textView(r.Properties)) {
						var passes []*mss.Properties
						for _, pr := range passRules {
							if pp, found := ruleFor(pr, cond); found {
								passes = append(passes, conv.textView(pp))
							}
						}
						v, ok = mergedAnchors(conv.textView(r.Properties), passes), true
					}
					if ok {
						isSet[name] = true
					} else {
						v = pc.def
					}
					propEntries[name] = append(propEntries[name], caseEntry{cond, v})
				}
			}
			r.Properties.SetDefaultInstance("")
		}

		zv := zoomValues{zoom: z, filter: caseExpr(filterEntries, false)}
		if zv.filter != false {
			if first < 0 {
				first = z
			}
			last, lastMapnik = z, zm
			zv.props = map[string]interface{}{}
			// In a fixed order, for a stable order of the notes.
			propNames := make([]string, 0, len(propEntries))
			for name := range propEntries {
				propNames = append(propNames, name)
			}
			sort.Strings(propNames)
			for _, name := range propNames {
				entries := propEntries[name]
				// A rule with the condition of an earlier one never
				// decides, so it cannot be the fallback either.
				entries = withoutShadowed(entries)
				n := len(entries)
				v := caseExpr(entries[:n-1], entries[n-1].value)
				if conv.props[name].zoomOnly && !equal(v, entries[n-1].value) {
					if !perFeature[name] {
						perFeature[name] = true
						m.note(layerID, g.name, instanceName(sym.Instance, name), Approximated,
							"differs between features, MapLibre takes one value per zoom level: the fallback value for all")
					}
					v = entries[n-1].value
				}
				zv.props[name] = v
			}
		}
		zooms = append(zooms, zv)
	}
	if first < 0 {
		return convertedLayer{}, false
	}

	// Only the zoom levels in which the layer draws are relevant.
	var inRange []zoomValues
	for _, zv := range zooms {
		if zv.zoom >= first && zv.zoom <= last {
			inRange = append(inRange, zv)
		}
	}

	id := layerID + "::" + g.name + "::" + instanceName(sym.Instance, strings.TrimSuffix(sym.Name, "-")+suffix)
	sl := convertedLayer{
		Layer: Layer{ID: id, Type: conv.layerType},
		first: first,
		last:  last,
	}
	if first > 0 {
		sl.MinZoom = intPtr(first)
	}
	if lastMapnik < m.opts.MaxMapnikZoom {
		sl.MaxZoom = intPtr(last + 1)
	}

	var filters []interface{}
	for _, zv := range inRange {
		filters = append(filters, zv.filter)
	}
	if f := stepExpr(first, filters); f != true {
		sl.Filter = f
	}

	names := make([]string, 0, len(conv.props))
	for name := range conv.props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pc := conv.props[name]
		if !isSet[name] && !pc.always {
			continue
		}
		var values []interface{}
		for _, zv := range inRange {
			if zv.props == nil {
				values = append(values, nil)
			} else {
				values = append(values, zv.props[name])
			}
		}
		fillGaps(values)
		v := stepExpr(first, values)
		if pc.layout {
			if sl.Layout == nil {
				sl.Layout = map[string]interface{}{}
			}
			sl.Layout[pc.target] = v
		} else {
			if sl.Paint == nil {
				sl.Paint = map[string]interface{}{}
			}
			sl.Paint[pc.target] = v
		}
	}

	for k, v := range conv.constLayout {
		if sl.Layout == nil {
			sl.Layout = map[string]interface{}{}
		}
		sl.Layout[k] = v
	}
	for k, v := range conv.constPaint {
		if sl.Paint == nil {
			sl.Paint = map[string]interface{}{}
		}
		sl.Paint[k] = v
	}

	fields := map[string]bool{}
	collectFields(sl.Filter, fields)
	collectFields(sl.Paint, fields)
	collectFields(sl.Layout, fields)
	for f := range fields {
		sl.fields = append(sl.fields, f)
	}
	sort.Strings(sl.fields)
	return sl, true
}

// fillGaps replaces nil values (zoom levels in which nothing is drawn) with
// the previous value, or the next one at the start, to keep steps short.
func fillGaps(values []interface{}) {
	var prev interface{}
	for i, v := range values {
		if v == nil {
			values[i] = prev
		} else {
			prev = v
		}
	}
	for i := len(values) - 1; i >= 0; i-- {
		if values[i] == nil {
			values[i] = prev
		} else {
			prev = values[i]
		}
	}
}

// caseExpr returns the expression for "the value of the first entry whose
// condition matches, else fallback", simplified.
func caseExpr(entries []caseEntry, fallback interface{}) interface{} {
	entries = withoutShadowed(entries)
	for i, e := range entries {
		if e.cond == true {
			entries, fallback = entries[:i], e.value
			break
		}
	}
	for len(entries) > 0 && equal(entries[len(entries)-1].value, fallback) {
		entries = entries[:len(entries)-1]
	}
	if len(entries) == 0 {
		return fallback
	}
	if expr, ok := matchExpr(entries, fallback); ok {
		return expr
	}
	plain := plainCaseExpr(entries, fallback)
	if expr, ok := hoistedMatch(entries, fallback); ok && len(mustJSON(expr)) < len(mustJSON(plain)) {
		return expr
	}
	return plain
}

// plainCaseExpr returns entries as a case chain, or as any if all values
// are true and the fallback is false.
func plainCaseExpr(entries []caseEntry, fallback interface{}) interface{} {
	if fallback == false {
		allTrue := true
		for _, e := range entries {
			if e.value != true {
				allTrue = false
				break
			}
		}
		if allTrue {
			if len(entries) == 1 {
				return entries[0].cond
			}
			expr := []interface{}{"any"}
			for _, e := range entries {
				expr = append(expr, e.cond)
			}
			return expr
		}
	}
	expr := []interface{}{"case"}
	for _, e := range entries {
		expr = append(expr, e.cond, e.value)
	}
	return append(expr, fallback)
}

// matchExpr rewrites entries whose conditions share all conjuncts but one
// ["==", ["get", field], literal] with the same field as a match on that
// field, which is exact and much shorter than the case chain.
func matchExpr(entries []caseEntry, fallback interface{}) (interface{}, bool) {
	if len(entries) < 2 {
		return nil, false
	}
	conjuncts := make([][]interface{}, len(entries))
	for i, e := range entries {
		if c, ok := e.cond.([]interface{}); ok && len(c) > 0 && c[0] == "all" {
			conjuncts[i] = c[1:]
		} else {
			conjuncts[i] = []interface{}{e.cond}
		}
	}

candidates:
	for _, c := range conjuncts[0] {
		field, _, ok := fieldEquals(c)
		if !ok {
			continue
		}
		var rest []interface{}
		labels := make([]interface{}, len(entries))
		for i := range entries {
			var others []interface{}
			found := false
			for _, cc := range conjuncts[i] {
				if f, lit, ok := fieldEquals(cc); ok && f == field && !found {
					labels[i] = lit
					found = true
				} else {
					others = append(others, cc)
				}
			}
			if !found {
				continue candidates
			}
			if i == 0 {
				rest = others
			} else if !equal(others, rest) {
				continue candidates
			}
			if _, isString := labels[i].(string); isString != isStringLabel(labels[0]) {
				continue candidates
			}
		}

		// Group the labels by value; the first entry with a label wins.
		type branch struct {
			labels []interface{}
			value  interface{}
		}
		var branches []*branch
		seenLabel := map[interface{}]bool{}
		for i, e := range entries {
			if seenLabel[labels[i]] {
				continue
			}
			seenLabel[labels[i]] = true
			var b *branch
			for _, bb := range branches {
				if equal(bb.value, e.value) {
					b = bb
					break
				}
			}
			if b == nil {
				b = &branch{value: e.value}
				branches = append(branches, b)
			}
			b.labels = append(b.labels, labels[i])
		}

		allTrue := fallback == false && len(branches) == 1 && branches[0].value == true
		match := []interface{}{"match", []interface{}{"get", field}}
		for _, b := range branches {
			if len(b.labels) == 1 {
				match = append(match, b.labels[0], b.value)
			} else {
				match = append(match, b.labels, b.value)
			}
		}
		match = append(match, fallback)

		switch {
		case len(rest) == 0:
			return match, true
		case allTrue:
			return append(append([]interface{}{"all"}, rest...), match), true
		case len(rest) == 1:
			return []interface{}{"case", rest[0], match, fallback}, true
		default:
			return []interface{}{"case", append([]interface{}{"all"}, rest...), match, fallback}, true
		}
	}
	return nil, false
}

func isStringLabel(v interface{}) bool {
	_, ok := v.(string)
	return ok
}

// fieldEquals reports whether c is ["==", ["get", field], literal] with a
// string or number literal.
func fieldEquals(c interface{}) (string, interface{}, bool) {
	expr, ok := c.([]interface{})
	if !ok || len(expr) != 3 || expr[0] != "==" {
		return "", nil, false
	}
	get, ok := expr[1].([]interface{})
	if !ok || len(get) != 2 || get[0] != "get" {
		return "", nil, false
	}
	field, ok := get[1].(string)
	if !ok {
		return "", nil, false
	}
	switch v := expr[2].(type) {
	case string:
		return field, v, true
	case float64:
		// MapLibre accepts only integers as numeric match labels.
		if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
			return field, v, true
		}
	}
	return "", nil, false
}

// stepExpr combines the values of consecutive zoom levels starting at first
// into a step expression over the zoom.
func stepExpr(first int, values []interface{}) interface{} {
	expr := []interface{}{"step", []interface{}{"zoom"}, values[0]}
	prev := values[0]
	for i, v := range values[1:] {
		if !equal(v, prev) {
			expr = append(expr, first+i+1, v)
			prev = v
		}
	}
	if len(expr) == 3 {
		return values[0]
	}
	return expr
}

func equal(a, b interface{}) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}

func intPtr(i int) *int { return &i }

// collectFields adds the names of all ["get", name] expressions in v.
func collectFields(v interface{}, fields map[string]bool) {
	switch v := v.(type) {
	case []interface{}:
		if len(v) == 2 && v[0] == "get" {
			if name, ok := v[1].(string); ok {
				fields[name] = true
				return
			}
		}
		for _, e := range v {
			collectFields(e, fields)
		}
	case map[string]interface{}:
		for _, e := range v {
			collectFields(e, fields)
		}
	}
}

// condition converts the filters of a rule to a MapLibre expression.
func (m *Map) condition(layerID, attachment string, filters []mss.Filter) interface{} {
	var conds []interface{}
	for _, f := range filters {
		c := m.filterExpr(layerID, attachment, f)
		if all, ok := c.([]interface{}); ok && len(all) > 0 && all[0] == "all" {
			conds = append(conds, all[1:]...)
		} else {
			conds = append(conds, c)
		}
	}
	conds = uniqueExprs(conds)
	switch len(conds) {
	case 0:
		return true
	case 1:
		return conds[0]
	default:
		return append([]interface{}{"all"}, conds...)
	}
}

// boolEquivalent returns the boolean that Mapnik considers equal to the
// numbers 0 and 1.
func boolEquivalent(v interface{}) (bool, bool) {
	switch v {
	case 0.0:
		return false, true
	case 1.0:
		return true, true
	}
	return false, false
}

var compOps = map[mss.CompOp]string{
	mss.EQ: "==", mss.NEQ: "!=", mss.GT: ">", mss.LT: "<", mss.GTE: ">=", mss.LTE: "<=",
}

func (m *Map) filterExpr(layerID, attachment string, f mss.Filter) interface{} {
	field := strings.Trim(f.Field, `"`)
	get := []interface{}{"get", field}

	switch v := f.Value.(type) {
	case mss.ModuloComparsion:
		op, ok := compOps[v.CompOp]
		if !ok {
			break
		}
		return []interface{}{"all",
			[]interface{}{"==", []interface{}{"typeof", get}, "number"},
			[]interface{}{op, []interface{}{"%", get, v.Div}, v.Value},
		}
	}

	op, ok := compOps[f.CompOp]
	if !ok {
		m.note(layerID, attachment, fmt.Sprintf("filter [%s %s ...]", field, f.CompOp), Approximated,
			"filter operator not supported, the rule never matches")
		return false
	}
	switch f.CompOp {
	case mss.EQ, mss.NEQ:
		// Mapnik compares booleans as 0 and 1; MapLibre's true != 1.
		if b, ok := boolEquivalent(f.Value); ok {
			join := "any"
			if f.CompOp == mss.NEQ {
				join = "all"
			}
			return []interface{}{join, []interface{}{op, get, f.Value}, []interface{}{op, get, b}}
		}
		return []interface{}{op, get, f.Value}
	}
	// Mapnik's ordering comparisons are false for missing values and other
	// types, MapLibre's raise an error.
	typ := "number"
	if _, ok := f.Value.(string); ok {
		typ = "string"
	}
	return []interface{}{"all",
		[]interface{}{"==", []interface{}{"typeof", get}, typ},
		[]interface{}{op, get, f.Value},
	}
}

// propValue returns the MapLibre value of a property for a rule.
func (m *Map) propValue(pc propConv, p *mss.Properties) (interface{}, bool) {
	if pc.mapValue != nil {
		return pc.mapValue(m, p)
	}
	return pc.value(p)
}

func (c symConverter) handles(property string) bool {
	for _, h := range c.handled {
		if h == property {
			return true
		}
	}
	return false
}

// uniqueExprs drops repeated expressions, e.g. the type checks of two
// comparisons on one field.
func uniqueExprs(exprs []interface{}) []interface{} {
	var result []interface{}
	seen := map[string]bool{}
	for _, e := range exprs {
		k := string(mustJSON(e))
		if !seen[k] {
			seen[k] = true
			result = append(result, e)
		}
	}
	return result
}

func mustJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// hoistedMatch rewrites entries as a match on the field that most of their
// conditions compare for equality with a literal. Each branch keeps, in
// order, the entries that can match a feature with that value, without the
// comparison; the fallback branch keeps the entries that do not compare the
// field. The result keeps the first-match semantics of the case chain and is
// much shorter for CartoCSS cross products like type × area.
func hoistedMatch(entries []caseEntry, fallback interface{}) (interface{}, bool) {
	conjunctsOf := func(cond interface{}) []interface{} {
		if c, ok := cond.([]interface{}); ok && len(c) > 0 && c[0] == "all" {
			return c[1:]
		}
		return []interface{}{cond}
	}
	counts := map[string]int{}
	var fields []string
	for _, e := range entries {
		seen := map[string]bool{}
		for _, c := range conjunctsOf(e.cond) {
			if f, _, ok := fieldEquals(c); ok && !seen[f] {
				seen[f] = true
				if counts[f] == 0 {
					fields = append(fields, f)
				}
				counts[f]++
			}
		}
	}
	field := ""
	for _, f := range fields {
		if counts[f] >= 2 && (field == "" || counts[f] > counts[field]) {
			field = f
		}
	}
	if field == "" {
		return nil, false
	}

	type split struct {
		label interface{} // nil: no comparison on field
		never bool        // compares field with two different labels
		rest  interface{}
	}
	splits := make([]split, len(entries))
	var labels []interface{}
	seenLabel := map[interface{}]bool{}
	for i, e := range entries {
		var others []interface{}
		for _, c := range conjunctsOf(e.cond) {
			f, lit, ok := fieldEquals(c)
			if !ok || f != field {
				others = append(others, c)
				continue
			}
			if splits[i].label != nil && !equal(splits[i].label, lit) {
				splits[i].never = true
			}
			splits[i].label = lit
		}
		if l := splits[i].label; l != nil && !splits[i].never {
			if len(labels) > 0 && isStringLabel(l) != isStringLabel(labels[0]) {
				return nil, false // MapLibre's match needs labels of one type
			}
			if !seenLabel[l] {
				seenLabel[l] = true
				labels = append(labels, l)
			}
		}
		switch len(others) {
		case 0:
			splits[i].rest = true
		case 1:
			splits[i].rest = others[0]
		default:
			splits[i].rest = append([]interface{}{"all"}, others...)
		}
	}
	if len(labels) < 2 {
		return nil, false
	}

	branch := func(label interface{}) interface{} {
		var sub []caseEntry
		for i, sp := range splits {
			if sp.never || (sp.label != nil && !equal(sp.label, label)) {
				continue
			}
			sub = append(sub, caseEntry{sp.rest, entries[i].value})
		}
		return caseExpr(sub, fallback)
	}
	def := branch(nil)

	type group struct {
		labels []interface{}
		value  interface{}
	}
	var groups []*group
	for _, l := range labels {
		v := branch(l)
		if equal(v, def) {
			continue
		}
		var g *group
		for _, gg := range groups {
			if equal(gg.value, v) {
				g = gg
				break
			}
		}
		if g == nil {
			g = &group{value: v}
			groups = append(groups, g)
		}
		g.labels = append(g.labels, l)
	}
	if len(groups) == 0 {
		return def, true
	}
	match := []interface{}{"match", []interface{}{"get", field}}
	for _, g := range groups {
		if len(g.labels) == 1 {
			match = append(match, g.labels[0], g.value)
		} else {
			match = append(match, g.labels, g.value)
		}
	}
	return append(match, def), true
}

// withoutShadowed drops entries whose condition an earlier entry already
// has: they never decide.
func withoutShadowed(entries []caseEntry) []caseEntry {
	var result []caseEntry
	seen := map[string]bool{}
	for _, e := range entries {
		k := string(mustJSON(e.cond))
		if !seen[k] {
			seen[k] = true
			result = append(result, e)
		}
	}
	return result
}
